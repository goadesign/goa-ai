package temporal

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNewAgentDataConverter_RoundTripsToolResult(t *testing.T) {
	dc := NewAgentDataConverter(func(tools.Ident) (*tools.ToolSpec, bool) { return nil, false })
	_, err := dc.ToPayload(&planner.ToolResult{Name: "test.tool"})
	require.Error(t, err)
}

func TestNewAgentDataConverter_DecodesToolResultsSetIntoSinglePointer(t *testing.T) {
	toolName := tools.Ident("test.tool")
	dc := NewAgentDataConverter(func(tools.Ident) (*tools.ToolSpec, bool) { return nil, false })
	p, err := dc.ToPayload(&api.ToolResultsSet{
		RunID: "run-123",
		ID:    "await-123",
		Results: []*api.ToolEvent{
			{
				Name:       toolName,
				ToolCallID: "tooluse-123",
				Result:     json.RawMessage(`{"value":"ok"}`),
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

func TestNewAgentDataConverter_RejectsJSONStringifiedToolResult(t *testing.T) {
	dc := NewAgentDataConverter(func(tools.Ident) (*tools.ToolSpec, bool) { return nil, false })
	_, err := dc.ToPayload(planner.ToolResult{Name: "test.tool", Result: `{"value":"ok"}`})
	require.Error(t, err)
}
