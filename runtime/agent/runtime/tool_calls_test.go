// tool_calls_test.go verifies tool-call envelope propagation across workflow and activity boundaries.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDispatchToolCallsPropagatesLabelsToActivityInput(t *testing.T) {
	wfCtx := &testWorkflowContext{ctx: context.Background()}
	exec := &toolBatchExec{
		r: &Runtime{
			toolsets: map[string]ToolsetRegistration{
				"svc.tools": {},
			},
			toolSpecs: map[tools.Ident]tools.ToolSpec{
				"search": newAnyJSONSpec("search", "svc.tools"),
			},
		},
		activityName: "execute",
		runID:        "run-1",
		agentID:      "svc.agent",
		sessionID:    "sess-1",
		turnID:       "turn-1",
		runCtx: &run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Labels: map[string]string{
				"aura.session.id": "sess-1",
				"kind":            "brief",
			},
		},
	}

	_, err := exec.dispatchToolCalls(wfCtx, []planner.ToolRequest{{
		Name:    tools.Ident("search"),
		Payload: rawjson.Message([]byte(`{"query":"status"}`)),
	}})
	require.NoError(t, err)
	require.NotNil(t, wfCtx.lastToolCall.Input)
	require.Equal(t, map[string]string{
		"aura.session.id": "sess-1",
		"kind":            "brief",
	}, wfCtx.lastToolCall.Input.Labels)
}
