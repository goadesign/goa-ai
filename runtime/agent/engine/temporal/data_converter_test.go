package temporal

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"

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

func TestNewAgentDataConverterRejectsToolResult(t *testing.T) {
	dc := NewAgentDataConverter()
	_, err := dc.ToPayload(&planner.ToolResult{Name: "test.tool"})
	require.Error(t, err)
}

func TestNewAgentDataConverterBoundsEveryPayloadEncoding(t *testing.T) {
	dc := NewAgentDataConverter()

	_, err := dc.ToPayload([]byte(strings.Repeat("x", engine.MaxPayloadBytes+1)))
	require.ErrorContains(t, err, "maximum aggregate size")

	_, err = dc.ToPayloads(
		[]byte(strings.Repeat("a", engine.MaxPayloadBytes/2)),
		[]byte(strings.Repeat("b", engine.MaxPayloadBytes/2+1)),
	)
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestPreflightTemporalValuesBoundsAggregateSource(t *testing.T) {
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
			err := preflightTemporalValues(test.values...)
			require.ErrorContains(t, err, "maximum aggregate size")
		})
	}
}

func TestPreflightTemporalValuesCountsNestedBytesAsOneBlock(t *testing.T) {
	for _, value := range []any{
		make([]byte, maxWorkflowJSONVisits+1),
		workflowByteSlice(make([]byte, maxWorkflowJSONVisits+1)),
	} {
		err := preflightTemporalValues(map[string]any{"value": value})
		require.NoError(t, err)
	}

	err := preflightTemporalValues(map[string]any{
		"value": make([]byte, engine.MaxPayloadBytes+1),
	})
	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestPreflightTemporalValuesRejectsCustomByteEncoding(t *testing.T) {
	err := preflightTemporalValues(hidingWorkflowByteSlice{1})
	require.ErrorContains(t, err, "unsupported custom JSON marshaler")

	err = preflightTemporalValues([]hidingWorkflowByte{1})
	require.ErrorContains(t, err, "unsupported custom JSON marshaler")
}

func TestNewAgentDataConverterRejectsOversizedPersistedPayload(t *testing.T) {
	err := NewAgentDataConverter().FromPayload(
		&commonpb.Payload{Data: []byte(strings.Repeat("x", engine.MaxPayloadBytes+1))},
		new([]byte),
	)

	require.ErrorContains(t, err, "maximum aggregate size")
}

func TestNewAgentDataConverterRejectsNestedToolResultInRunInput(t *testing.T) {
	dc := NewAgentDataConverter()
	_, err := dc.ToPayload(&api.RunInput{
		AgentID: "test.agent",
		RunID:   "run-123",
		Metadata: map[string]any{
			"nested": &planner.ToolResult{Name: "test.tool"},
		},
	})

	require.ErrorContains(t, err, "planner.ToolResult must not cross workflow boundaries")
}

func TestNewAgentDataConverterRejectsNestedTypedNilToolResult(t *testing.T) {
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
			_, err := NewAgentDataConverter().ToPayload(test.value)
			require.ErrorContains(t, err, "planner.ToolResult must not cross workflow boundaries")
		})
	}
}

func TestNewAgentDataConverterRejectsHidingMarshalers(t *testing.T) {
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
			_, err := NewAgentDataConverter().ToPayload(test.value)
			require.ErrorContains(t, err, test.kind)
		})
	}
}

func TestNewAgentDataConverterChecksEmbeddedPrivateStructFields(t *testing.T) {
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
			_, err := NewAgentDataConverter().ToPayload(test.value)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestNewAgentDataConverterDecodesToolResultsSetIntoSinglePointer(t *testing.T) {
	toolName := tools.Ident("test.tool")
	dc := NewAgentDataConverter()
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

func TestNewAgentDataConverterRoundTripsPlanActivityInputToolOutputs(t *testing.T) {
	t.Parallel()

	dc := NewAgentDataConverter()
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

func TestNewAgentDataConverterRoundTripsOutputContractFailure(t *testing.T) {
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
	dc := NewAgentDataConverter()
	payload, err := dc.ToPayload(expected)
	require.NoError(t, err)

	var decoded *api.PlanActivityOutput
	require.NoError(t, dc.FromPayload(payload, &decoded))
	require.Equal(t, expected, decoded)
}

func TestNewAgentDataConverterRejectsJSONStringifiedToolResult(t *testing.T) {
	dc := NewAgentDataConverter()
	_, err := dc.ToPayload(planner.ToolResult{Name: "test.tool", Result: `{"value":"ok"}`})
	require.Error(t, err)
}

func TestNewAgentDataConverterRejectsObsoletePolicyFields(t *testing.T) {
	dc := NewAgentDataConverter()
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

func TestNewAgentDataConverterDecodesHistoricalFailureCapField(t *testing.T) {
	dc := NewAgentDataConverter()
	terminalCall := map[string]any{
		"Name":    "service.complete",
		"Payload": map[string]any{"status": "stopped"},
	}
	payload, err := dc.ToPayload(map[string]any{
		"AgentID": "test.agent",
		"RunID":   "run-123",
		"Policy": map[string]any{
			"MaxConsecutiveFailedToolCalls": 2,
			"LimitTerminalPlans": map[string]any{
				"TimeBudget":        terminalCall,
				"ToolCallCap":       terminalCall,
				"FailedToolCallCap": terminalCall,
			},
		},
	})
	require.NoError(t, err)

	var decoded *api.RunInput
	require.NoError(t, dc.FromPayload(payload, &decoded))
	require.Equal(t, 2, decoded.Policy.MaxRecoveryTurns)
	require.Equal(t, tools.Ident("service.complete"), decoded.Policy.LimitTerminalPlans.RecoveryCap.Name)
	require.JSONEq(t, `{"status":"stopped"}`, string(decoded.Policy.LimitTerminalPlans.RecoveryCap.Payload))
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
