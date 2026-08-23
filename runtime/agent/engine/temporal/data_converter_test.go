package temporal

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
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
)

func TestNewAgentDataConverterRejectsToolResult(t *testing.T) {
	dc := NewAgentDataConverter()
	_, err := dc.ToPayload(&planner.ToolResult{Name: "test.tool"})
	require.Error(t, err)
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
