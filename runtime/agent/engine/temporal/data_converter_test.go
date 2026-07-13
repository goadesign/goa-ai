package temporal

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNewAgentDataConverterRejectsToolResult(t *testing.T) {
	dc := NewAgentDataConverter()
	_, err := dc.ToPayload(&planner.ToolResult{Name: "test.tool"})
	require.Error(t, err)
}

func TestNewAgentDataConverterDecodesToolResultsSetIntoSinglePointer(t *testing.T) {
	toolName := tools.Ident("test.tool")
	dc := NewAgentDataConverter()
	p, err := dc.ToPayload(&api.ToolResultsSet{
		RunID: "run-123",
		ID:    "await-123",
		Results: []*api.ProvidedToolResult{
			{
				Name:       toolName,
				ToolCallID: "tooluse-123",
				Result:     rawjson.Message([]byte(`{"value":"ok"}`)),
			},
		},
	})
	require.NoError(t, err)

	var decoded *api.ToolResultsSet
	require.NoError(t, dc.FromPayload(p, &decoded))
	require.NotNil(t, decoded)
	require.Len(t, decoded.Results, 1)
	require.Equal(t, toolName, decoded.Results[0].Name)
	require.JSONEq(t, `{"value":"ok"}`, string(decoded.Results[0].Result))
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
