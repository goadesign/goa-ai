package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestBuildNextResumeRequestKeepsLargePayloadAndResultsOffWire(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent.budget", &stubPlanner{})
	base := &planner.PlanInput{
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			},
		},
		RunContext: run.Context{
			RunID:   "run-1",
			Attempt: 1,
		},
	}
	payload := rawjson.Message([]byte(`{"blob":"` + strings.Repeat("x", maxPlanActivityInputBytes) + `"}`))
	result := rawjson.Message([]byte(`{"blob":"` + strings.Repeat("x", maxPlanActivityInputBytes) + `"}`))
	serverData := rawjson.Message([]byte(`[{"kind":"test.kind","data":"` + strings.Repeat("x", maxPlanActivityInputBytes) + `"}]`))
	toolOutputs := []*planner.ToolOutput{
		{
			Name:        "svc.ts.big",
			ToolCallID:  "tc-1",
			Payload:     payload,
			Result:      result,
			ResultBytes: len(result),
			ServerData:  serverData,
		},
	}
	nextAttempt := 2

	in, err := rt.buildNextResumeRequest(agent.Ident("service.agent.budget"), base, nil, toolOutputs, &nextAttempt)
	require.NoError(t, err)
	require.Len(t, in.ToolOutputs, 1)
	require.Equal(t, "tc-1", in.ToolOutputs[0].ToolCallID)

	wire, err := json.Marshal(in)
	require.NoError(t, err)
	require.Less(t, len(wire), 10_000, "resume request wire shape must stay compact")
}

func TestToolResultContentTruncatesOversizedResults(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	name := tools.Ident("svc.ts.big")
	rt.toolSpecs[name] = newAnyJSONSpec(name, "svc.ts")

	tr := &planner.ToolResult{
		Name:       name,
		ToolCallID: "tc-1",
		Result: map[string]any{
			"blob": strings.Repeat("x", transcript.MaxToolResultContentBytes+1024),
		},
	}

	content, err := rt.toolResultContent(nil, tr)
	require.NoError(t, err)
	m, ok := content.(map[string]any)
	require.True(t, ok, "oversized tool_result content must be projected, not raw JSON")
	require.Equal(t, true, m["omitted"])
	require.Equal(t, "size_limit", m["reason"])
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
