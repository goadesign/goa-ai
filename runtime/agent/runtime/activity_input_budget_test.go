package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestEncodeToolEventsForPlanningOmitsArtifactsAndLargeResults(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent.budget", &stubPlanner{})
	name := tools.Ident("svc.ts.big")
	rt.toolSpecs[name] = newAnyJSONSpec(name, "svc.ts")

	tr := &planner.ToolResult{
		Name:       name,
		ToolCallID: "tc-1",
		Result: map[string]any{
			"blob": strings.Repeat("x", maxPlanToolResultBytes+1024),
		},
		Artifacts: []*planner.Artifact{
			{
				Kind: "artifact_kind",
				Data: map[string]any{"ignored": true},
			},
		},
	}

	events, err := rt.encodeToolEventsForPlanning(context.Background(), []*planner.ToolResult{tr})
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, name, events[0].Name)
	require.Empty(t, events[0].Result, "planning tool events must omit oversized results")
	require.True(t, events[0].ResultOmitted)
	require.Equal(t, resultOmittedReasonWorkflowBudget, events[0].ResultOmittedReason)
	require.Greater(t, events[0].ResultBytes, maxPlanToolResultBytes)
	require.Nil(t, events[0].Artifacts, "planning tool events must omit artifacts")
}

func TestToolResultContentTruncatesOversizedResults(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	name := tools.Ident("svc.ts.big")
	rt.toolSpecs[name] = newAnyJSONSpec(name, "svc.ts")

	tr := &planner.ToolResult{
		Name:       name,
		ToolCallID: "tc-1",
		Result: map[string]any{
			"blob": strings.Repeat("x", maxTranscriptToolResultBytes+1024),
		},
	}

	content, err := rt.toolResultContent(tr)
	require.NoError(t, err)
	m, ok := content.(map[string]any)
	require.True(t, ok, "oversized tool_result content must be projected, not raw JSON")
	require.Equal(t, true, m["truncated"])
	note, ok := m["note"].(string)
	require.True(t, ok, "oversized tool_result projection must include a note")
	require.NotEmpty(t, note)
}

func TestEnforcePlanActivityInputBudgetFailsFast(t *testing.T) {
	in := PlanActivityInput{
		RunID: "run-1",
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: strings.Repeat("x", maxPlanActivityInputBytes+1024)}},
			},
		},
	}
	require.Error(t, enforcePlanActivityInputBudget(in))
}
