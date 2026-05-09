package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
			ExampleInput: map[string]any{
				"summary": map[string]any{
					"summary": "Headline",
				},
				"recommendations":      []any{"Do X"},
				"requires_remediation": true,
			},
		},
	}

	hint := buildRetryHintFromDecodeError(decodeTypeError{inner: ute}, tools.Ident("diagnostics.emit.emit_diagnosis_result"), spec)
	require.NotNil(t, hint)
	require.Equal(t, planner.RetryReasonMissingFields, hint.Reason)
	require.Equal(t, tools.Ident("diagnostics.emit.emit_diagnosis_result"), hint.Tool)
	require.Equal(t, []string{"summary"}, hint.MissingFields)
	require.NotEmpty(t, hint.ClarifyingQuestion)
	require.Contains(t, hint.ClarifyingQuestion, "summary")
	require.NotNil(t, hint.ExampleInput)
	// ExampleInput should contain the top-level summary object.
	s, ok := hint.ExampleInput["summary"]
	require.True(t, ok)
	require.NotNil(t, s)
}

// TestBuildRetryHintFromDecodeError_SyntaxError verifies that malformed JSON
// yields a RetryHint with $payload marked as missing.
func TestBuildRetryHintFromDecodeError_SyntaxError(t *testing.T) {
	se := &json.SyntaxError{Offset: 10}
	hint := buildRetryHintFromDecodeError(se, tools.Ident("svc.ts.tool"), nil)
	require.NotNil(t, hint)
	require.Equal(t, planner.RetryReasonMissingFields, hint.Reason)
	require.Equal(t, []string{"$payload"}, hint.MissingFields)
	require.NotEmpty(t, hint.ClarifyingQuestion)
}

// TestBuildRetryHintFromDecodeError_GenericErrorReturnsNil verifies that
// buildRetryHintFromDecodeError preserves its strict contract: errors raised
// outside json.SyntaxError/UnmarshalTypeError yield nil so non-decode callers
// (for example agent-tool prompt rendering) can propagate the error verbatim.
func TestBuildRetryHintFromDecodeError_GenericErrorReturnsNil(t *testing.T) {
	decErr := errors.New(`unexpected Rule2 type "signal_change"`)
	spec := &tools.ToolSpec{
		Payload: tools.TypeSpec{
			ExampleInput: map[string]any{"AssistantText": "ok"},
		},
	}
	require.Nil(t, buildRetryHintFromDecodeError(decErr, tools.Ident("svc.ts.tool"), spec))
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
					Name: "P",
					ExampleInput: map[string]any{
						"summary": map[string]any{
							"summary": "Headline",
						},
						"recommendations":      []any{"Do X"},
						"requires_remediation": true,
					},
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
	require.Equal(t, []string{"summary"}, out.RetryHint.MissingFields)
	require.NotNil(t, out.RetryHint.ExampleInput)
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
	require.Equal(t, []string{"type"}, out.RetryHint.MissingFields)
	require.Contains(t, out.RetryHint.ClarifyingQuestion, "one of: schedule, signal")
}
