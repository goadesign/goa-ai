// tool_calls_test.go verifies tool-call envelope propagation across workflow and activity boundaries.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNewToolCallScheduledEventPreservesRuntimeCorrelation(t *testing.T) {
	t.Parallel()

	event := newToolCallScheduledEvent(
		"run-1",
		"svc.agent",
		"session-1",
		ToolCall{
			Name:                       "tools.continue_search",
			ToolCallID:                 "continue-1",
			Payload:                    rawjson.Message(`{"cursor":"next"}`),
			ContinuationRootToolCallID: "source-1",
		},
		"tools",
		"parent-1",
		2,
	)

	require.Equal(t, "source-1", event.ContinuationRootToolCallID)
	require.Equal(t, "parent-1", event.ParentToolCallID)
	require.Equal(t, 2, event.ExpectedChildrenTotal)
}

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

	_, err := exec.dispatchToolCalls(wfCtx, []ToolCall{{
		ToolCallID: "search-call",
		Name:       tools.Ident("search"),
		Payload:    rawjson.Message([]byte(`{"query":"status"}`)),
	}})
	require.NoError(t, err)
	require.NotNil(t, wfCtx.lastToolCall.Input)
	require.Equal(t, map[string]string{
		"aura.session.id": "sess-1",
		"kind":            "brief",
	}, wfCtx.lastToolCall.Input.Labels)
}
