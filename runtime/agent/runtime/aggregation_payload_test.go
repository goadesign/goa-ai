package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestBuildAggregationSummary(t *testing.T) {
	input := FinalizerInput{
		Parent: ParentCall{
			ToolName:   tools.Ident("ada.method"),
			ToolCallID: "parent-123",
		},
		Children: []ChildCall{
			{ToolName: tools.Ident("child.ok"), Status: "ok", Result: map[string]any{"v": 1}},
			{ToolName: tools.Ident("child.err"), Error: planner.NewToolError("failed")},
		},
	}
	summary := BuildAggregationSummary(input)
	require.Equal(t, tools.Ident("ada.method"), summary.Method)
	require.Equal(t, "parent-123", summary.ToolCallID)
	require.Len(t, summary.Children, 2)
	require.Equal(t, tools.Ident("child.ok"), summary.Children[0].Tool)
	require.Equal(t, "ok", summary.Children[0].Status)
	require.Equal(t, map[string]any{"v": 1}, summary.Children[0].Result)
	require.Equal(t, tools.Ident("child.err"), summary.Children[1].Tool)
	require.Equal(t, "error", summary.Children[1].Status)
	require.Equal(t, "failed", summary.Children[1].Error)
}
