package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestBuildAggregationFacts(t *testing.T) {
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
	facts := BuildAggregationFacts(input)
	require.Equal(t, tools.Ident("ada.method"), facts.Method)
	require.Equal(t, "parent-123", facts.ToolCallID)
	require.Len(t, facts.Children, 2)
	require.Equal(t, tools.Ident("child.ok"), facts.Children[0].Tool)
	require.Equal(t, "ok", facts.Children[0].Status)
	require.Equal(t, map[string]any{"v": 1}, facts.Children[0].Result)
	require.Equal(t, tools.Ident("child.err"), facts.Children[1].Tool)
	require.Equal(t, "error", facts.Children[1].Status)
	require.Equal(t, "failed", facts.Children[1].Error)
}
