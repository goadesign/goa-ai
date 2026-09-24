package runtime

// This file checks that direct agent-child calls keep their parent open until
// the child finishes cancellation, regardless of the workflow engine.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestExecuteAgentChildWaitsAfterParentCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	runtime := New(newTestStore())
	_, err := createSessionForTest(t.Context(), runtime.Store, "session-1")
	require.NoError(t, err)
	seedEndID, err := publishLiteralHistory(ctx, runtime.Store, storage.SeedDeclaration{
		AgentID: "child.agent", RunID: "child-run", SessionID: "session-1",
		Kind: storage.SeedLiteral,
	}, nil)
	require.NoError(t, err)
	childHandles := make(chan *controlledChildHandle, 1)
	wfCtx := &testWorkflowContext{
		ctx:                    ctx,
		hookRuntime:            runtime,
		controlledChildHandles: childHandles,
	}
	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, err := runtime.executeAgentChild(
			wfCtx,
			testAgentDefinition("child.agent", "child.workflow", "child.queue", nil, nil),
			agentChildRequest{seedEndID: seedEndID, runContext: run.Context{
				RunID:            "child-run",
				SessionID:        "session-1",
				TurnID:           "turn-1",
				ParentRunID:      "parent-run",
				ParentAgentID:    "parent.agent",
				ParentToolCallID: "call-1",
				Tool:             "child.tools.run",
			}},
		)
		done <- result{err: err}
	}()

	handle := waitForChildHandle(t, childHandles, "direct child")
	cancel()
	select {
	case <-done:
		require.Fail(t, "parent returned before the child finished cancellation")
	case <-time.After(20 * time.Millisecond):
	}
	close(handle.ready)
	require.NoError(t, (<-done).err)
}

func TestExecuteAgentChildRejectsMissingRequiredLabelBeforeStart(t *testing.T) {
	childHandles := make(chan *controlledChildHandle, 1)
	wfCtx := &testWorkflowContext{
		ctx:                    t.Context(),
		controlledChildHandles: childHandles,
	}
	runtime := &Runtime{}
	definition := testAgentDefinition(
		"child.agent", "child.workflow", "child.queue", nil, []string{"facility_id"})

	_, err := runtime.executeAgentChild(
		wfCtx,
		definition,
		agentChildRequest{runContext: run.Context{
			RunID:            "child-run",
			SessionID:        "session-1",
			TurnID:           "turn-1",
			ParentRunID:      "parent-run",
			ParentAgentID:    "parent.agent",
			ParentToolCallID: "call-1",
			Tool:             "parent.tools.child",
		}},
	)

	require.ErrorIs(t, err, ErrMissingLabels)
	select {
	case <-childHandles:
		require.Fail(t, "child workflow started after required-label rejection")
	default:
	}
}
