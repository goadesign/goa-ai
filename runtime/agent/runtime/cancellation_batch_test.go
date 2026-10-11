package runtime

// These tests interrupt real tool batches after earlier work completes or
// fails. Saved schedules, completed results, and earlier errors must survive
// cancellation, and accepted remote work must still receive cleanup.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	cancellationResultStore struct {
		*testStore
		resultError   error
		resultAttempt chan struct{}
	}
	cancellationTimerFailure struct {
		engine.WorkflowContext
		err error
	}
)

func TestCancellationRetainsCompletedChildAndWaitsForSibling(t *testing.T) {
	for _, failPublication := range []bool{false, true} {
		name := "completed result"
		if failPublication {
			name = "earlier publication error"
		}
		t.Run(name, func(t *testing.T) {
			prior := errors.New("child result persistence unavailable")
			store := &cancellationResultStore{testStore: newTestStore(), resultAttempt: make(chan struct{}, 1)}
			if failPublication {
				store.resultError = prior
			}
			rt := New(store)
			recorder := &recordingHooks{ch: make(chan hooks.Event, 32)}
			rt.Bus = recorder
			spec := newAnyJSONSpec("children.tools.run")
			spec.IsAgentTool = true
			spec.AgentID = "child.agent"
			registration := NewAgentToolsetRegistration(AgentToolConfig{
				Definition:       testAgentDefinition("child.agent", "child.workflow", "child.queue", nil, nil),
				Name:             "children.tools",
				AgentToolContent: AgentToolContent{Prompt: func(tools.Ident, any) string { return invokePromptText }},
			})
			rt.toolsets[registration.Name] = registration
			seedTestToolset(rt, registration.Name, spec)
			handles := make(chan *controlledChildHandle, 3)
			wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, controlledChildHandles: handles}
			runCtx := run.Context{RunID: "parent-run", SessionID: "session", TurnID: "turn"}
			seedParentRun(t, rt.Store, runCtx.RunID, runCtx.SessionID)
			history := testToolHistory(t, rt, "parent.agent", runCtx, nil)
			calls := []ToolCall{
				{Name: spec.Name, ToolCallID: "completed"},
				{Name: spec.Name, ToolCallID: "canceled"},
				{Name: spec.Name, ToolCallID: "pending"},
			}
			type batchResult struct {
				outcomes []*ToolExecutionResult
				timedOut bool
				err      error
			}
			done := make(chan batchResult, 1)
			go func() {
				outcomes, timedOut, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{}, "parent.agent", &runCtx, history, calls, 0, nil, time.Time{}, nil)
				done <- batchResult{outcomes, timedOut, err}
			}()
			completed := waitForChildHandle(t, handles, "completed child")
			canceled := waitForChildHandle(t, handles, "canceled child")
			pending := waitForChildHandle(t, handles, "pending child")
			close(completed.ready)
			select {
			case <-store.resultAttempt:
			case <-time.After(time.Second):
				require.FailNow(t, "completed child did not attempt result publication")
			}
			canceled.err = context.Canceled
			close(canceled.ready)
			require.Eventually(t, pending.wasCanceled, time.Second, time.Millisecond)
			select {
			case <-done:
				require.FailNow(t, "batch returned before pending child settled")
			default:
			}
			close(pending.ready)
			got := <-done
			require.ErrorIs(t, got.err, context.Canceled)
			assert.False(t, got.timedOut)
			expectedOutcomes := 1
			if failPublication {
				require.ErrorIs(t, got.err, prior)
				expectedOutcomes = 3
			}
			require.Len(t, got.outcomes, expectedOutcomes)
			assert.Equal(t, "completed", got.outcomes[0].ToolResult.ToolCallID)
			assert.True(t, got.outcomes[0].schedulePublished)
			assert.Equal(t, !failPublication, got.outcomes[0].resultPublished)
			assert.False(t, completed.wasCanceled())
		})
	}
}

func TestCancellationRetainsEarlierBatchError(t *testing.T) {
	for _, phase := range []string{"dispatch", "collection"} {
		t.Run(phase, func(t *testing.T) {
			prior := errors.New("result persistence unavailable")
			store := &cancellationResultStore{testStore: newTestStore()}
			if phase == "collection" {
				store.resultError = prior
			}
			rt := New(store)
			first := newAnyJSONSpec("first.tools.read")
			second := newAnyJSONSpec("second.tools.read")
			rt.toolsets["first.tools"] = ToolsetRegistration{Inline: phase == "dispatch", DecodeInExecutor: true,
				Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					if phase == "dispatch" {
						return nil, prior
					}
					return successfulToolResult(call), nil
				}),
			}
			rt.toolsets["second.tools"] = ToolsetRegistration{DecodeInExecutor: true,
				Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) { return nil, context.Canceled },
			}
			seedTestToolset(rt, "first.tools", first)
			seedTestToolset(rt, "second.tools", second)
			wf := &testWorkflowContext{ctx: t.Context(), runtime: rt, hookRuntime: rt}
			runCtx := run.Context{RunID: "error-run", SessionID: "session", TurnID: "turn"}
			history := testToolHistory(t, rt, "parent.agent", runCtx, nil)
			outcomes, timedOut, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{}, "parent.agent", &runCtx, history,
				[]ToolCall{{Name: first.Name, ToolCallID: "first", Payload: []byte(`{}`)}, {Name: second.Name, ToolCallID: "second", Payload: []byte(`{}`)}}, 0, nil, time.Time{}, nil)
			require.ErrorIs(t, err, prior)
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, runStatusFailed, terminalRunStatusForError(err))
			assert.False(t, timedOut)
			require.Len(t, outcomes, 2)
			for _, outcome := range outcomes {
				assert.True(t, outcome.schedulePublished)
			}
		})
	}
}

func TestSavedTaskCleanupBeforeDispatch(t *testing.T) {
	for _, phase := range []string{"expired", "timer error"} {
		t.Run(phase, func(t *testing.T) {
			rt := New(newTestStore())
			spec := newAnyJSONSpec("remote.tools.read")
			seedTestToolset(rt, "remote.tools", spec)
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			cleanups := 0
			wf := &routeWorkflowContext{ctx: t.Context(), now: func() time.Time { return now }, hookRuntime: rt,
				toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
					require.NoError(t, ctx.Err())
					id, canceled := input.ExecutionContinuation.AsTaskCancel()
					assert.True(t, canceled)
					assert.Equal(t, "saved-task", id)
					cleanups++
					pending, err := tooloperation.NewPendingTaskWait(id, nil)
					require.NoError(t, err)
					return &ToolOutput{PendingExecution: pending}, nil
				}},
			}
			var workflow engine.WorkflowContext = wf
			deadline := now
			timerErr := errors.New("timer scheduling unavailable")
			if phase == "timer error" {
				deadline = now.Add(time.Second)
				workflow = &cancellationTimerFailure{WorkflowContext: wf, err: timerErr}
			}
			runCtx := run.Context{RunID: "saved-run", SessionID: "session", TurnID: "turn"}
			history := testToolHistory(t, rt, "parent.agent", runCtx, nil)
			_, timedOut, err := rt.executeToolCalls(workflow, "execute", engine.ActivityOptions{}, "parent.agent", &runCtx, history,
				[]ToolCall{{Name: spec.Name, ToolCallID: "saved-call", Payload: []byte(`{}`)}}, 0, nil, deadline,
				map[string]*taskExecution{"saved-call": {TaskID: "saved-task"}})
			if phase == "timer error" {
				require.ErrorIs(t, err, timerErr)
				assert.False(t, timedOut)
			} else {
				require.NoError(t, err)
				assert.True(t, timedOut)
			}
			assert.Equal(t, 1, cleanups)
		})
	}
}

// AppendRunRecord rejects result publication at the store boundary while
// preserving schedule writes, so later cancellation must retain this error.
func (s *cancellationResultStore) AppendRunRecord(ctx context.Context, event *runlog.Event) (storage.AppendResult, error) {
	if event.Type == hooks.ToolResultReceived && s.resultAttempt != nil {
		s.resultAttempt <- struct{}{}
	}
	if event.Type == hooks.ToolResultReceived && s.resultError != nil {
		return storage.AppendResult{}, s.resultError
	}
	return s.testStore.AppendRunRecord(ctx, event)
}

// NewTimer rejects timer creation before dispatch so the test can verify that
// already-owned remote work still receives its cancellation operation.
func (w *cancellationTimerFailure) NewTimer(context.Context, time.Duration) (engine.Future[time.Time], error) {
	return nil, w.err
}
