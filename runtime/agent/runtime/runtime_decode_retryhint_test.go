package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// decodeTypeError wraps json.UnmarshalTypeError so tests can exercise
// buildRetryHintFromDecodeError without relying on the panic-prone Error
// method when Type is unset.
type decodeTypeError struct {
	inner *json.UnmarshalTypeError
}

func (e decodeTypeError) Error() string {
	return "decode error"
}

func (e decodeTypeError) Unwrap() error {
	return e.inner
}

// TestBuildRetryHintFromDecodeError_UnmarshalTypeError verifies that a JSON
// type mismatch produces a RetryHint with MissingFields, ReasonMissingFields,
// and an attached example when available.
func TestBuildRetryHintFromDecodeError_UnmarshalTypeError(t *testing.T) {
	// Simulate a type error on the "summary" field.
	ute := &json.UnmarshalTypeError{Field: "summary"}
	spec := &tools.ToolSpec{
		Payload: tools.TypeSpec{
			ExampleJSON: tools.RawJSON(`{"summary":{"summary":"Headline"},"recommendations":["Do X"],"requires_remediation":true}`),
		},
	}

	hint := buildRetryHintFromDecodeError(decodeTypeError{inner: ute}, tools.Ident("diagnostics.emit.emit_diagnosis_result"), spec)
	require.NotNil(t, hint)
	require.Equal(t, planner.RetryReasonMissingFields, hint.Reason)
	require.Equal(t, tools.Ident("diagnostics.emit.emit_diagnosis_result"), hint.Tool)
	require.True(t, hint.RestrictToTool)
	require.Equal(t, []string{"summary"}, hint.MissingFields)
	require.NotEmpty(t, hint.ClarifyingQuestion)
	require.Contains(t, hint.ClarifyingQuestion, "summary")
	require.JSONEq(t, `{"summary":{"summary":"Headline"},"recommendations":["Do X"],"requires_remediation":true}`, string(hint.ExampleJSON))
}

// TestBuildRetryHintFromDecodeError_SyntaxError verifies that malformed JSON
// yields a RetryHint with $payload marked as missing.
func TestBuildRetryHintFromDecodeError_SyntaxError(t *testing.T) {
	se := &json.SyntaxError{Offset: 10}
	hint := buildRetryHintFromDecodeError(se, tools.Ident("svc.ts.tool"), nil)
	require.NotNil(t, hint)
	require.Equal(t, planner.RetryReasonMissingFields, hint.Reason)
	require.True(t, hint.RestrictToTool)
	require.Equal(t, []string{"$payload"}, hint.MissingFields)
	require.NotEmpty(t, hint.ClarifyingQuestion)
}

// TestBuildRetryHintFromDecodeError_GenericDecodeErrorRestrictsRetry verifies
// that decode failures without a JSON type or syntax shape — truncated
// payloads (io.EOF / io.ErrUnexpectedEOF) and codec-specific errors — still
// produce a restricted retry hint. Every error at this boundary is a defect in
// the model-authored payload, so the model must always get a chance to resend
// it; returning nil here previously escalated agent-tool decode failures into
// terminal workflow errors.
func TestBuildRetryHintFromDecodeError_GenericDecodeErrorRestrictsRetry(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{name: "truncated payload", err: io.ErrUnexpectedEOF},
		{name: "empty payload", err: io.EOF},
		{name: "codec union error", err: errors.New(`unexpected Rule2 type "signal_change"`)},
	}
	spec := &tools.ToolSpec{
		Payload: tools.TypeSpec{
			ExampleJSON: tools.RawJSON(`{"AssistantText":"ok"}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint := buildRetryHintFromDecodeError(tc.err, tools.Ident("svc.ts.tool"), spec)
			require.NotNil(t, hint)
			require.Equal(t, tools.Ident("svc.ts.tool"), hint.Tool)
			require.True(t, hint.RestrictToTool)
			require.Equal(t, []string{"$payload"}, hint.MissingFields)
			require.Contains(t, hint.ClarifyingQuestion, "svc.ts.tool")
			require.Contains(t, hint.ClarifyingQuestion, tc.err.Error())
			require.JSONEq(t, `{"AssistantText":"ok"}`, string(hint.ExampleJSON))
		})
	}
}

// TestBuildRetryHintFromAgentToolRequestError_ClassifiesPayloadDefectsOnly
// verifies the agent-tool request error router: payload decode failures (the
// model authored an undecodable payload) return a restricted retry hint so the
// parent run feeds the failure back to the model, while runtime configuration
// errors return nil and stay terminal workflow errors.
func TestBuildRetryHintFromAgentToolRequestError_ClassifiesPayloadDefectsOnly(t *testing.T) {
	decodeErr := fmt.Errorf("decode agent tool payload for ada.count_events: %w",
		&agentToolPayloadError{cause: io.EOF})
	hint := buildRetryHintFromAgentToolRequestError(decodeErr, tools.Ident("ada.count_events"), nil)
	require.NotNil(t, hint)
	require.True(t, hint.RestrictToTool)
	require.Equal(t, tools.Ident("ada.count_events"), hint.Tool)

	configErr := errors.New("agent tool ada.count_events requires a registered ToolSpec for payload decoding (missing specs/codecs)")
	require.Nil(t, buildRetryHintFromAgentToolRequestError(configErr, tools.Ident("ada.count_events"), nil))
}

// TestExecuteToolActivity_DecodeErrorRetryHint ensures ExecuteToolActivity
// returns a ToolOutput with a RetryHint when payload decoding fails.
func TestExecuteToolActivity_DecodeErrorRetryHint(t *testing.T) {
	rt := &Runtime{
		logger:   telemetry.NoopLogger{},
		toolsets: make(map[string]ToolsetRegistration),
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"svc.ts.tool": {
				Name:    "svc.ts.tool",
				Service: "svc",
				Toolset: "svc.ts",
				Payload: tools.TypeSpec{
					Name:        "P",
					ExampleJSON: tools.RawJSON(`{"summary":{"summary":"Headline"},"recommendations":["Do X"],"requires_remediation":true}`),
					Codec: tools.JSONCodec[any]{
						FromJSON: func(data []byte) (any, error) {
							// Force a decode failure that buildRetryHintFromDecodeError
							// can interpret, wrapped to avoid invoking the panic-prone
							// UnmarshalTypeError.Error implementation in tests.
							return nil, decodeTypeError{inner: &json.UnmarshalTypeError{Field: "summary"}}
						},
					},
				},
				Result: tools.TypeSpec{Name: "R"},
			},
		},
	}
	rt.toolsets["svc.ts"] = ToolsetRegistration{
		Name: "svc.ts",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			t.Fatalf("executor should not be called when pre-decode fails")
			return nil, nil
		}),
		Specs: []tools.ToolSpec{
			rt.toolSpecs["svc.ts.tool"],
		},
	}

	raw := rawjson.Message([]byte(`{"summary":"wrong"}`))
	input := ToolInput{
		ToolsetName: "svc.ts",
		ToolName:    tools.Ident("svc.ts.tool"),
		Payload:     raw,
	}

	out, err := rt.ExecuteToolActivity(context.Background(), &input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotEmpty(t, out.Error)
	require.NotNil(t, out.RetryHint)
	require.Equal(t, planner.RetryReasonMissingFields, out.RetryHint.Reason)
	require.True(t, out.RetryHint.RestrictToTool)
	require.Equal(t, []string{"summary"}, out.RetryHint.MissingFields)
	require.NotNil(t, out.RetryHint.ExampleJSON)
}

// TestExecuteToolActivity_UnionValidationRetryHint verifies that generated
// union discriminator errors stay structured when wrapped by the payload codec.
func TestExecuteToolActivity_UnionValidationRetryHint(t *testing.T) {
	rt := &Runtime{
		logger:   telemetry.NoopLogger{},
		toolsets: make(map[string]ToolsetRegistration),
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"svc.ts.tool": {
				Name:    "svc.ts.tool",
				Service: "svc",
				Toolset: "svc.ts",
				Payload: tools.TypeSpec{
					Name: "P",
					Codec: tools.JSONCodec[any]{
						FromJSON: func(data []byte) (any, error) {
							err := &fakeValidationError{
								issues: []*tools.FieldIssue{{
									Field:      "type",
									Constraint: "invalid_enum_value",
									Allowed:    []string{"schedule", "signal"},
								}},
							}
							return nil, fmt.Errorf("decode payload: %w", err)
						},
					},
				},
				Result: tools.TypeSpec{Name: "R"},
			},
		},
	}
	rt.toolsets["svc.ts"] = ToolsetRegistration{
		Name: "svc.ts",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			t.Fatalf("executor should not be called when generated validation fails")
			return nil, nil
		}),
		Specs: []tools.ToolSpec{
			rt.toolSpecs["svc.ts.tool"],
		},
	}

	out, err := rt.ExecuteToolActivity(context.Background(), &ToolInput{
		ToolsetName: "svc.ts",
		ToolName:    tools.Ident("svc.ts.tool"),
		Payload:     rawjson.Message([]byte(`{"type":"signal_change"}`)),
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotEmpty(t, out.Error)
	require.NotNil(t, out.RetryHint)
	require.Equal(t, planner.RetryReasonInvalidArguments, out.RetryHint.Reason)
	require.True(t, out.RetryHint.RestrictToTool)
	require.Equal(t, []string{"type"}, out.RetryHint.MissingFields)
	require.Contains(t, out.RetryHint.ClarifyingQuestion, "one of: schedule, signal")
}

func TestExecuteToolActivity_UnknownFieldRetryHint(t *testing.T) {
	rt := &Runtime{
		logger:   telemetry.NoopLogger{},
		toolsets: make(map[string]ToolsetRegistration),
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"atlas.discover.get_device_status": {
				Name:    "atlas.discover.get_device_status",
				Service: "atlas",
				Toolset: "atlas.discover",
				Payload: tools.TypeSpec{
					Name: "P",
					Codec: tools.JSONCodec[any]{
						FromJSON: func(data []byte) (any, error) {
							err := &fakeValidationError{
								issues: []*tools.FieldIssue{{
									Field:      "scope_context",
									Constraint: "unknown_field",
									Allowed:    []string{"device_alias"},
								}},
							}
							return nil, fmt.Errorf("decode payload: %w", err)
						},
					},
				},
				Result: tools.TypeSpec{Name: "R"},
			},
		},
	}
	rt.toolsets["atlas.discover"] = ToolsetRegistration{
		Name: "atlas.discover",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			t.Fatalf("executor should not be called when generated validation fails")
			return nil, nil
		}),
		Specs: []tools.ToolSpec{
			rt.toolSpecs["atlas.discover.get_device_status"],
		},
	}

	out, err := rt.ExecuteToolActivity(context.Background(), &ToolInput{
		ToolsetName: "atlas.discover",
		ToolName:    tools.Ident("atlas.discover.get_device_status"),
		Payload:     rawjson.Message([]byte(`{"scope_context":"compressor_2"}`)),
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotEmpty(t, out.Error)
	require.NotNil(t, out.RetryHint)
	require.Equal(t, planner.RetryReasonInvalidArguments, out.RetryHint.Reason)
	require.True(t, out.RetryHint.RestrictToTool)
	require.Equal(t, []string{"scope_context"}, out.RetryHint.MissingFields)
	require.Contains(t, out.RetryHint.ClarifyingQuestion, "remove `scope_context`")
	require.Contains(t, out.RetryHint.ClarifyingQuestion, "`device_alias`")
}
