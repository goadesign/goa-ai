// These tests prove every workflow engine shares strict encoding, byte limits,
// and ownership of accepted payload bytes.
package workflowcodec

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// hidingJSONMarshaler could conceal a workflow-only value from ordinary
	// reflection if custom marshaling ran before preflight.
	hidingJSONMarshaler struct {
		hidden any
	}

	// hidingTextMarshaler proves text encoding receives the same treatment.
	hidingTextMarshaler struct {
		hidden any
	}

	// workflowByteSlice proves named byte slices use the same byte accounting
	// as plain []byte values.
	workflowByteSlice []byte

	// workflowStringKey proves maps may use named keys whose underlying kind is
	// string because JSON preserves those keys without conversion.
	workflowStringKey string

	// hidingWorkflowByteSlice proves a byte slice cannot hide its contents
	// behind custom JSON.
	hidingWorkflowByteSlice []byte

	// hidingWorkflowByte proves a slice with custom-encoded byte elements is
	// inspected element by element.
	hidingWorkflowByte byte

	// privateWorkflowFields verifies that preflight inspects exported fields
	// promoted by an embedded private struct exactly as encoding/json does.
	privateWorkflowFields struct {
		Raw    rawjson.Message
		Number float64
		Text   string
	}

	workflowEnvelope struct {
		privateWorkflowFields
	}

	privateWorkflowResult struct {
		Result *planner.ToolResult
	}

	workflowResultEnvelope struct {
		privateWorkflowResult
	}
)

func TestNewDataConverterRejectsToolResult(t *testing.T) {
	dc := NewDataConverter()
	_, err := dc.ToPayload(&planner.ToolResult{Name: "test.tool"})
	require.Error(t, err)
}

func TestNewDataConverterBoundsEveryPayloadEncoding(t *testing.T) {
	dc := NewDataConverter()

	_, err := dc.ToPayload([]byte(strings.Repeat("x", engine.MaxPayloadBytes+1)))
	require.ErrorContains(t, err, "maximum aggregate size")

	_, err = dc.ToPayloads(
		[]byte(strings.Repeat("a", engine.MaxPayloadBytes/2)),
		[]byte(strings.Repeat("b", engine.MaxPayloadBytes/2+1)),
	)
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestNewDataConverterCopiesRawValues(t *testing.T) {
	source := &commonpb.Payload{
		Metadata: map[string][]byte{"encoding": []byte("binary/plain")},
		Data:     []byte("value"),
	}
	encoded, err := NewDataConverter().ToPayload(converter.NewRawValue(source))
	require.NoError(t, err)
	source.Metadata["encoding"][0] = 'X'
	source.Data[0] = 'X'
	require.Equal(t, []byte("binary/plain"), encoded.Metadata["encoding"])
	require.Equal(t, []byte("value"), encoded.Data)

	var decoded converter.RawValue
	require.NoError(t, NewDataConverter().FromPayload(encoded, &decoded))
	encoded.Metadata["encoding"][0] = 'Y'
	encoded.Data[0] = 'Y'
	require.Equal(t, []byte("binary/plain"), decoded.Payload().Metadata["encoding"])
	require.Equal(t, []byte("value"), decoded.Payload().Data)
}

func TestNewDataConverterRejectsNilRawPayloads(t *testing.T) {
	codec := NewDataConverter()
	raw := converter.NewRawValue(nil)

	_, err := codec.ToPayload(raw)
	require.EqualError(t, err, "workflow codec: raw payload is nil")

	_, err = codec.ToPayloads("first", raw)
	require.EqualError(t, err, "workflow codec: raw payload is nil")

	var decoded converter.RawValue
	err = codec.FromPayload(nil, &decoded)
	require.EqualError(t, err, "workflow codec: payload is nil")
}

func TestNewDataConverterRequiresOneDestinationPerPayload(t *testing.T) {
	codec := NewDataConverter()
	one, err := codec.ToPayloads("one")
	require.NoError(t, err)
	two, err := codec.ToPayloads("one", "two")
	require.NoError(t, err)
	var decoded string
	require.NoError(t, codec.FromPayloads(one, &decoded))
	require.Equal(t, "one", decoded)

	tests := []struct {
		name         string
		payloads     *commonpb.Payloads
		destinations []any
		wantErr      string
	}{
		{name: "no payloads and no destinations"},
		{
			name:         "missing stored payload",
			payloads:     one,
			destinations: []any{new(string), new(string)},
			wantErr:      "workflow codec: payload count 1 does not match destination count 2",
		},
		{
			name:         "extra stored payload",
			payloads:     two,
			destinations: []any{new(string)},
			wantErr:      "workflow codec: payload count 2 does not match destination count 1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := codec.FromPayloads(test.payloads, test.destinations...)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, test.wantErr)
		})
	}
}

func TestNewDataConverterValidatesRawValueAtExactLimit(t *testing.T) {
	codec := NewDataConverter()
	_, err := codec.ToPayload(converter.NewRawValue(&commonpb.Payload{
		Data: make([]byte, engine.MaxPayloadBytes),
	}))
	require.NoError(t, err)

	_, err = codec.ToPayload(converter.NewRawValue(&commonpb.Payload{
		Data: make([]byte, engine.MaxPayloadBytes+1),
	}))
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestNewDataConverterRejectsOversizedRawValueBeforeCopy(t *testing.T) {
	data := make([]byte, engine.MaxPayloadBytes+1)
	raw := converter.NewRawValue(&commonpb.Payload{Data: data})
	result := testing.Benchmark(func(b *testing.B) {
		codec := NewDataConverter()
		for range b.N {
			if _, err := codec.ToPayload(raw); err == nil {
				b.Fatal("expected oversized raw value to fail")
			}
		}
	})
	require.Less(t, result.AllocedBytesPerOp(), int64(len(data)))
}

func TestPreflightValuesBoundsAggregateSource(t *testing.T) {
	tests := []struct {
		name   string
		values []any
	}{
		{
			name: "nested string",
			values: []any{map[string]any{
				"value": strings.Repeat("x", engine.MaxPayloadBytes+1),
			}},
		},
		{
			name: "multiple payloads",
			values: []any{
				strings.Repeat("a", engine.MaxPayloadBytes/2),
				strings.Repeat("b", engine.MaxPayloadBytes/2+1),
			},
		},
		{
			name:   "raw JSON",
			values: []any{rawjson.Message(`"` + strings.Repeat("x", engine.MaxPayloadBytes+1) + `"`)},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := preflightValues(test.values...)
			require.ErrorContains(t, err, "maximum aggregate size")
		})
	}
}

func TestWorkflowCodecRejectsInvalidUTF8BeforeEncoding(t *testing.T) {
	invalid := string([]byte{0xff})
	tests := []struct {
		name  string
		value any
	}{
		{name: "string", value: invalid},
		{name: "nested string", value: map[string]any{"value": []any{invalid}}},
		{name: "map key", value: map[string]any{invalid: "value"}},
		{name: "runtime raw JSON", value: rawjson.Message{'"', 0xff, '"'}},
		{name: "standard raw JSON", value: json.RawMessage{'"', 0xff, '"'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDataConverter().ToPayload(test.value)
			require.ErrorContains(t, err, "invalid UTF-8")
		})
	}
}

func TestWorkflowCodecRequiresStringMapKeys(t *testing.T) {
	codec := NewDataConverter()

	_, err := codec.ToPayload(map[int]string{1: "one"})
	require.ErrorContains(t, err, "map key type int must have underlying kind string")

	payload, err := codec.ToPayload(map[workflowStringKey]string{"one": "first"})
	require.NoError(t, err)
	require.JSONEq(t, `{"one":"first"}`, string(payload.Data))
}

func TestBudgetRejectsInvalidUTF8TextAndMetadataKeys(t *testing.T) {
	invalid := string([]byte{0xff})
	budget := new(Budget)
	require.ErrorContains(t, budget.AddText(invalid), "invalid UTF-8")
	require.ErrorContains(t, budget.AddPayload(&commonpb.Payload{
		Metadata: map[string][]byte{invalid: []byte("value")},
	}), "invalid UTF-8")
}

func TestPreflightValuesCountsNestedBytesAsOneBlock(t *testing.T) {
	for _, value := range []any{
		make([]byte, maxWorkflowJSONVisits+1),
		workflowByteSlice(make([]byte, maxWorkflowJSONVisits+1)),
	} {
		err := preflightValues(map[string]any{"value": value})
		require.NoError(t, err)
	}

	err := preflightValues(map[string]any{
		"value": make([]byte, engine.MaxPayloadBytes+1),
	})
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestPreflightValuesRejectsCustomByteEncoding(t *testing.T) {
	err := preflightValues(hidingWorkflowByteSlice{1})
	require.ErrorContains(t, err, "unsupported custom JSON marshaler")

	err = preflightValues([]hidingWorkflowByte{1})
	require.ErrorContains(t, err, "unsupported custom JSON marshaler")
}

func TestNewDataConverterRejectsOversizedPersistedPayload(t *testing.T) {
	err := NewDataConverter().FromPayload(
		&commonpb.Payload{Data: []byte(strings.Repeat("x", engine.MaxPayloadBytes+1))},
		new([]byte),
	)

	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestNewDataConverterRejectsInvalidUTF8PersistedJSON(t *testing.T) {
	payload := &commonpb.Payload{
		Metadata: map[string][]byte{"encoding": []byte("json/plain")},
		Data:     []byte{'"', 0xff, '"'},
	}
	var decoded string
	err := NewDataConverter().FromPayload(payload, &decoded)
	require.EqualError(t, err, "workflow codec: canonical JSON payload contains invalid UTF-8")
}

func TestNewDataConverterRejectsNestedToolResultInRunInput(t *testing.T) {
	dc := NewDataConverter()
	_, err := dc.ToPayload(&api.RunInput{
		AgentID: "test.agent",
		RunID:   "run-123",
		Metadata: map[string]any{
			"nested": &planner.ToolResult{Name: "test.tool"},
		},
	})

	require.ErrorContains(t, err, "planner.ToolResult must not cross workflow boundaries")
}

func TestNewDataConverterRejectsNestedTypedNilToolResult(t *testing.T) {
	var typedNil *planner.ToolResult
	tests := []struct {
		name  string
		value any
	}{
		{name: "map", value: map[string]any{"nested": typedNil}},
		{name: "slice", value: []any{map[string]any{"nested": typedNil}}},
		{name: "struct", value: struct{ Nested any }{Nested: typedNil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDataConverter().ToPayload(test.value)
			require.ErrorContains(t, err, "planner.ToolResult must not cross workflow boundaries")
		})
	}
}

func TestNewDataConverterRejectsHidingMarshalers(t *testing.T) {
	var typedNil *planner.ToolResult
	tests := []struct {
		name  string
		value any
		kind  string
	}{
		{
			name:  "JSON",
			value: map[string]any{"nested": hidingJSONMarshaler{hidden: typedNil}},
			kind:  "custom JSON marshaler",
		},
		{
			name:  "text",
			value: map[string]any{"nested": hidingTextMarshaler{hidden: typedNil}},
			kind:  "custom text marshaler",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDataConverter().ToPayload(test.value)
			require.ErrorContains(t, err, test.kind)
		})
	}
}

func TestNewDataConverterChecksEmbeddedPrivateStructFields(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		wantErr string
	}{
		{
			name: "workflow-only value",
			value: workflowResultEnvelope{privateWorkflowResult: privateWorkflowResult{
				Result: &planner.ToolResult{Name: "test.tool"},
			}},
			wantErr: "planner.ToolResult must not cross workflow boundaries",
		},
		{
			name: "invalid raw JSON",
			value: workflowEnvelope{privateWorkflowFields: privateWorkflowFields{
				Raw: rawjson.Message(`{"broken"`),
			}},
			wantErr: "invalid raw JSON",
		},
		{
			name: "non-finite number",
			value: workflowEnvelope{privateWorkflowFields: privateWorkflowFields{
				Number: math.Inf(1),
			}},
			wantErr: "non-finite number",
		},
		{
			name: "oversized string",
			value: workflowEnvelope{privateWorkflowFields: privateWorkflowFields{
				Text: strings.Repeat("x", engine.MaxPayloadBytes+1),
			}},
			wantErr: "maximum aggregate size",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewDataConverter().ToPayload(test.value)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestNewDataConverterDecodesToolResultsSetIntoSinglePointer(t *testing.T) {
	toolName := tools.Ident("test.tool")
	dc := NewDataConverter()
	p, err := dc.ToPayload(&api.ToolResultsSet{
		ID: "await-123",
		Results: []*api.ProvidedToolResult{
			{
				Name:       toolName,
				ToolCallID: "tooluse-123",
				Success: &api.ProvidedToolSuccess{
					Result: rawjson.Message([]byte(`{"value":"ok"}`)),
				},
			},
		},
	})
	require.NoError(t, err)

	var decoded *api.ToolResultsSet
	require.NoError(t, dc.FromPayload(p, &decoded))
	require.NotNil(t, decoded)
	require.Len(t, decoded.Results, 1)
	require.Equal(t, toolName, decoded.Results[0].Name)
	require.NotNil(t, decoded.Results[0].Success)
	require.JSONEq(t, `{"value":"ok"}`, string(decoded.Results[0].Success.Result))
}

func TestNewDataConverterRoundTripsPlanActivityInputToolOutputs(t *testing.T) {
	t.Parallel()

	dc := NewDataConverter()
	p, err := dc.ToPayload(&api.PlanActivityInput{
		AgentID: "test.agent",
		RunID:   "run-123",
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
		RunContext: run.Context{
			RunID:   "run-123",
			Attempt: 2,
		},
		ToolOutputs: []*api.ToolOutputRef{
			{
				ToolCallID: "call-1",
			},
		},
	})
	require.NoError(t, err)

	var decoded *api.PlanActivityInput
	require.NoError(t, dc.FromPayload(p, &decoded))
	require.NotNil(t, decoded)
	require.Len(t, decoded.ToolOutputs, 1)
	require.Equal(t, "call-1", decoded.ToolOutputs[0].ToolCallID)
}

func TestNewDataConverterRoundTripsOutputContractFailure(t *testing.T) {
	t.Parallel()

	const (
		reasonDigest   = "edb2b946a1a981532214c45073c74f275c977d2ff8970681b4f3ae63b2c5b331"
		responseDigest = "a4d26868017c0ccffe2efe50944ef4211834660cca83452c6f0c77b3f202d121"
	)
	expected := &api.PlanActivityOutput{
		PublicationBatchID: "93b75cbb-80a7-4f91-a66e-17e66609aa67",
		Usage: model.TokenUsage{
			Model:        "provider-model",
			ModelClass:   model.ModelClass("large"),
			InputTokens:  11,
			OutputTokens: 7,
			TotalTokens:  18,
		},
		OutputContractFailure: &api.OutputContractFailure{
			Origin:                          planner.OutputContractOriginModel,
			ReasonSHA256:                    reasonDigest,
			ReasonSize:                      6,
			ModelResponsePresent:            true,
			ModelResponseFingerprintVersion: api.ModelResponseFingerprintVersionV1,
			ModelResponseSHA256:             responseDigest,
			ModelResponseSize:               42,
		},
	}
	dc := NewDataConverter()
	payload, err := dc.ToPayload(expected)
	require.NoError(t, err)

	var decoded *api.PlanActivityOutput
	require.NoError(t, dc.FromPayload(payload, &decoded))
	require.Equal(t, expected, decoded)
}

func TestNewDataConverterRejectsJSONStringifiedToolResult(t *testing.T) {
	dc := NewDataConverter()
	_, err := dc.ToPayload(planner.ToolResult{Name: "test.tool", Result: `{"value":"ok"}`})
	require.Error(t, err)
}

func TestNewDataConverterRejectsObsoletePolicyFields(t *testing.T) {
	dc := NewDataConverter()
	payload, err := dc.ToPayload(map[string]any{
		"AgentID": "test.agent",
		"RunID":   "run-123",
		"Policy": map[string]any{
			"AllowedTags": []string{"obsolete"},
		},
	})
	require.NoError(t, err)

	var decoded *api.RunInput
	require.ErrorContains(t, dc.FromPayload(payload, &decoded), `unknown field "AllowedTags"`)
}

// MarshalJSON hides every field to prove preflight rejects the custom encoder
// before it can conceal its typed value.
func (m hidingJSONMarshaler) MarshalJSON() ([]byte, error) {
	if m.hidden == nil {
		return nil, errors.New("hidden JSON value is required")
	}
	return []byte("null"), nil
}

// MarshalText hides every field to prove preflight rejects text encoders too.
func (m hidingTextMarshaler) MarshalText() ([]byte, error) {
	if m.hidden == nil {
		return nil, errors.New("hidden text value is required")
	}
	return []byte("hidden"), nil
}

// MarshalText proves JSON map encoding uses the underlying string key and does
// not replace it with custom text.
func (key workflowStringKey) MarshalText() ([]byte, error) {
	if key == "" {
		return nil, errors.New("key is required")
	}
	return []byte("replaced"), nil
}

// MarshalJSON would hide a named byte slice if preflight let encoding/json call
// it.
func (value hidingWorkflowByteSlice) MarshalJSON() ([]byte, error) {
	if len(value) == 0 {
		return nil, errors.New("byte slice is required")
	}
	return []byte("null"), nil
}

// MarshalJSON would replace a byte element if preflight treated its containing
// slice as an ordinary byte block.
func (value *hidingWorkflowByte) MarshalJSON() ([]byte, error) {
	if value == nil {
		return nil, errors.New("byte is required")
	}
	return []byte("0"), nil
}
