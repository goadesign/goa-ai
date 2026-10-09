// These tests run cancellation through published workflow inputs and the real
// storage activity. An ended session forbids ordinary work while the accepted
// workflow remains responsible for inherited Task cleanup and its final record.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	// admissionCancellationWorkflowContext delivers cancellation immediately
	// after start storage, before the workflow restores or publishes prompts.
	admissionCancellationWorkflowContext struct {
		*testWorkflowContext
		triggered  *bool
		afterStart func()
	}

	// questionCancellationBus cancels the named run after its question is
	// published, so tests observe ownership between execution and suspension.
	questionCancellationBus struct {
		hooks.Bus
		runtime *Runtime
		runID   string
		kind    hooks.EventType
	}
)

func (w *admissionCancellationWorkflowContext) Context() context.Context {
	return engine.WithWorkflowContext(w.ctx, w)
}

func (w *admissionCancellationWorkflowContext) Detached() engine.WorkflowContext {
	return &admissionCancellationWorkflowContext{
		testWorkflowContext: w.testWorkflowContext.Detached().(*testWorkflowContext),
		triggered:           w.triggered, afterStart: w.afterStart,
	}
}

func (w *admissionCancellationWorkflowContext) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	out, err := w.testWorkflowContext.ExecuteStorageActivity(call)
	if err == nil && call.Command.RootStart != nil && !*w.triggered {
		*w.triggered = true
		w.afterStart()
	}
	return out, err
}

func (b *questionCancellationBus) Publish(ctx context.Context, event hooks.Event) error {
	if err := b.Bus.Publish(ctx, event); err != nil {
		return err
	}
	if event.RunID() != b.runID || event.Type() != b.kind {
		return nil
	}
	if err := b.runtime.CancelRun(context.WithoutCancel(ctx), CancelRequest{RunID: b.runID, Reason: run.CancellationReasonUserRequested}); err != nil {
		return err
	}
	return context.Canceled
}

func TestCancellationDuringFirstTaskQuestionSettlesAcceptedTask(t *testing.T) {
	spec := newAnyJSONSpec("remote.tools.lookup")
	definition := NewAgentDefinition(AgentRoute{ID: "test.agent", WorkflowName: "test.workflow", DefaultTaskQueue: "test.queue"},
		[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, nil)
	bus := &questionCancellationBus{Bus: hooks.NewBus(), runID: "source", kind: hooks.AwaitMCPInput}
	rt := New(newTestStore(), WithEngine(engineinmem.New()), WithHooks(bus))
	bus.runtime = rt
	var creates, cancels atomic.Int32
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "remote.tools", Specs: []tools.ToolSpec{spec}, DecodeInExecutor: true,
		Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			assert.Equal(t, "source", call.RunID)
			if operation := call.ExecutionContinuation; operation != nil {
				id, cancel := operation.AsTaskCancel()
				assert.True(t, cancel)
				assert.Empty(t, id)
				assert.EqualValues(t, 1, call.ExecutionSequence)
				cancels.Add(1)
				pending, err := tooloperation.NewPendingTaskWait(id, nil)
				if err != nil {
					return nil, err
				}
				return Unfinished(pending)
			}
			creates.Add(1)
			pending, err := tooloperation.NewPendingTaskInput("", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
				"": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
			}})
			if err != nil {
				return nil, err
			}
			return Unfinished(pending)
		},
	}))
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "plan", ExecuteToolActivity: "execute", ResumeActivityName: "resume",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: spec.Name, Payload: rawjson.Message(`{"query":"original"}`)}}}, nil
		}},
	}))
	_, err := createSessionForTest(t.Context(), rt.Store, "session")
	require.NoError(t, err)
	_, err = rt.MustClient(definition.route.ID).Run(t.Context(), "session", nil, WithRunID("source"), WithTurnID("turn"))
	require.ErrorIs(t, err, context.Canceled)
	assert.EqualValues(t, 1, creates.Load())
	assert.EqualValues(t, 1, cancels.Load())
	meta, err := rt.Store.LoadRun(t.Context(), "source")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, meta.Status)
	assert.Equal(t, run.CancellationReasonUserRequested, meta.CancellationReason)
	_, err = rt.LoadRunSuspension(t.Context(), "source")
	assert.ErrorIs(t, err, session.ErrRunSuspensionNotFound)
}

func TestEndedSessionWorkflowStoresItsOwnTerminalResult(t *testing.T) {
	rt := New(newTestStore(), WithEngine(engineinmem.New()))
	definition := testAgentDefinition("test.agent", "test.workflow", "test.queue", nil, nil)
	var plans atomic.Int32
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "plan", ExecuteToolActivity: "execute", ResumeActivityName: "resume",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			plans.Add(1)
			return nil, errors.New("an ended session must not plan")
		}},
	}))
	_, err := createSessionForTest(t.Context(), rt.Store, "session")
	require.NoError(t, err)
	_, err = rt.Store.(interface {
		EndSession(context.Context, string, time.Time) (session.Session, error)
	}).EndSession(t.Context(), "session", time.Now())
	require.NoError(t, err)
	_, err = rt.MustClient(definition.route.ID).Run(t.Context(), "session", nil, WithRunID("ended-run"))
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, plans.Load())
	meta, err := rt.Store.LoadRun(t.Context(), "ended-run")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, meta.Status)
	assert.Equal(t, run.CancellationReasonSessionEnded, meta.CancellationReason)
	page, err := rt.ListRunEvents(t.Context(), meta.RunID, "", 20)
	require.NoError(t, err)
	assert.Equal(t, 1, countRunEventsByType(page, hooks.RunCompleted))
	assert.Zero(t, countRunEventsByType(page, hooks.PromptRendered))
}

func TestContinuationCancellationSettlesSavedTaskAfterWorkerReplacement(t *testing.T) {
	for _, ended := range []bool{true, false} {
		t.Run(fmt.Sprintf("ended_session=%t", ended), func(t *testing.T) {
			testContinuationCancellationAfterWorkerReplacement(t, ended, false)
		})
	}
}

func TestCancelRunSettlesSuspendedTaskAfterWorkerReplacement(t *testing.T) {
	testContinuationCancellationAfterWorkerReplacement(t, false, true)
}

func testContinuationCancellationAfterWorkerReplacement(t *testing.T, ended, useJob bool) {
	spec := newAnyJSONSpec("remote.tools.lookup")
	definition := NewAgentDefinition(AgentRoute{ID: "test.agent", WorkflowName: "test.workflow", DefaultTaskQueue: "test.queue"},
		[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, nil)
	store := newTestStore()
	var plans, creates, cancels atomic.Int32
	install := func() *Runtime {
		rt := New(store, WithEngine(engineinmem.New()))
		require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
			Name: "remote.tools", Specs: []tools.ToolSpec{spec}, DecodeInExecutor: true,
			Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
				assert.Equal(t, "source", call.RunID)
				assert.JSONEq(t, `{"query":"original"}`, string(call.Payload))
				if operation := call.ExecutionContinuation; operation != nil {
					id, cancel := operation.AsTaskCancel()
					assert.True(t, cancel)
					assert.Empty(t, id)
					assert.EqualValues(t, 1, call.ExecutionSequence)
					cancels.Add(1)
					pending, err := tooloperation.NewPendingTaskWait(id, nil)
					if err != nil {
						return nil, err
					}
					return Unfinished(pending)
				}
				creates.Add(1)
				pending, err := tooloperation.NewPendingTaskInput("", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
					"": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
				}})
				if err != nil {
					return nil, err
				}
				return Unfinished(pending)
			},
		}))
		require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
			Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
			PlanActivityName: "plan", ExecuteToolActivity: "execute", ResumeActivityName: "resume",
			Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				plans.Add(1)
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: spec.Name, Payload: rawjson.Message(`{"query":"original"}`)}}}, nil
			}},
		}))
		return rt
	}
	rt := install()
	_, err := createSessionForTest(t.Context(), store, "session")
	require.NoError(t, err)
	first, err := rt.MustClient(definition.route.ID).Run(t.Context(), "session", nil, WithRunID("source"), WithTurnID("turn-1"))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	response := &api.PendingInputResponse{
		MCP: &api.MCPInputResponse{ToolCallID: first.Suspension.Pending[0].MCP.ToolCallID, Responses: map[string]json.RawMessage{
			"": json.RawMessage(`{"action":"accept","content":{}}`),
		}},
	}
	reason := run.CancellationReasonUserRequested
	if ended {
		reason = run.CancellationReasonSessionEnded
		_, err = store.EndSession(t.Context(), "session", time.Now())
		require.NoError(t, err)
	}
	rt = install()
	cleanupRunID := "cleanup"
	switch {
	case useJob:
		require.NoError(t, rt.CancelRun(t.Context(), CancelRequest{RunID: "source", Reason: reason}))
		require.NoError(t, rt.CancelRun(t.Context(), CancelRequest{RunID: "source", Reason: reason}))
		require.Eventually(t, func() bool {
			previous, loadErr := store.LoadRun(t.Context(), "source")
			if loadErr != nil || previous.SuccessorRunID == "" {
				return false
			}
			meta, loadErr := store.LoadRun(t.Context(), previous.SuccessorRunID)
			if loadErr != nil || meta.Status != session.RunStatusCanceled {
				return false
			}
			cleanupRunID = meta.RunID
			return true
		}, 5*time.Second, time.Millisecond)
		err = context.Canceled
	case ended:
		_, err = rt.MustClient(definition.route.ID).Continue(t.Context(), "session", "source", "cleanup", "turn-2", response, WorkflowOptions{})
	default:
		input, writer, buildErr := rt.buildStoredContinuationRunInput(t.Context(), definition, "session", "source", "cleanup", "turn-2", &api.RunContinuationInput{Response: response}, "cleanup", "cleanup")
		require.NoError(t, buildErr)
		compiled, encodeErr := json.Marshal(input)
		require.NoError(t, encodeErr)
		require.NoError(t, writer.publish(t.Context(), compiled))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		base := &testWorkflowContext{ctx: ctx, runtime: rt, hookRuntime: rt}
		triggered := false
		wfCtx := &admissionCancellationWorkflowContext{testWorkflowContext: base, triggered: &triggered}
		wfCtx.afterStart = func() {
			require.NoError(t, base.cancellationHandler(wfCtx, engine.CancellationRequest{RunID: "cleanup", Reason: reason}))
			cancel()
		}
		_, err = rt.ExecuteWorkflow(wfCtx, input)
	}
	require.ErrorIs(t, err, context.Canceled)
	assert.EqualValues(t, 1, creates.Load())
	assert.EqualValues(t, 1, plans.Load())
	assert.EqualValues(t, 1, cancels.Load())
	meta, err := store.LoadRun(t.Context(), cleanupRunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, meta.Status)
	assert.Equal(t, reason, meta.CancellationReason)
	previous, err := store.LoadRun(t.Context(), "source")
	require.NoError(t, err)
	assert.Equal(t, cleanupRunID, previous.SuccessorRunID)
	page, err := rt.ListRunEvents(t.Context(), cleanupRunID, "", 20)
	require.NoError(t, err)
	assert.Equal(t, 1, countRunEventsByType(page, hooks.RunCompleted))
	assert.Zero(t, countRunEventsByType(page, hooks.ToolResultReceived))
}

func TestCancellationDuringTaskAnswerSettlesSelectedAndSavedTasks(t *testing.T) {
	for _, source := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel active answer", true: "follow accepted answer from suspended source"}[source], func(t *testing.T) {
			testCancellationDuringTaskAnswer(t, source)
		})
	}
}

func testCancellationDuringTaskAnswer(t *testing.T, source bool) {
	spec := newAnyJSONSpec("remote.tools.lookup")
	definition := NewAgentDefinition(AgentRoute{ID: "test.agent", WorkflowName: "test.workflow", DefaultTaskQueue: "test.queue"},
		[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, nil)
	store := newTestStore()
	var creates, updates, cancels atomic.Int32
	updateStarted := make(chan struct{})
	rt := New(store, WithEngine(engineinmem.New()))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "remote.tools", Specs: []tools.ToolSpec{spec}, DecodeInExecutor: true,
		Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			assert.Equal(t, "source", call.RunID)
			if operation := call.ExecutionContinuation; operation != nil {
				if id, _, update := operation.AsTaskUpdate(); update {
					assert.Equal(t, "task/"+call.ToolCallID, id)
					assert.EqualValues(t, 1, call.ExecutionSequence)
					updates.Add(1)
					close(updateStarted)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				id, cancel := operation.AsTaskCancel()
				assert.True(t, cancel)
				assert.Equal(t, "task/"+call.ToolCallID, id)
				assert.Contains(t, []uint64{1, 2}, call.ExecutionSequence)
				cancels.Add(1)
				pending, err := tooloperation.NewPendingTaskWait(id, nil)
				if err != nil {
					return nil, err
				}
				return Unfinished(pending)
			}
			creates.Add(1)
			pending, err := tooloperation.NewPendingTaskInput("task/"+call.ToolCallID, nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
				"": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
			}})
			if err != nil {
				return nil, err
			}
			return Unfinished(pending)
		},
	}))
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "plan", ExecuteToolActivity: "execute", ResumeActivityName: "resume",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{
				{Name: spec.Name, Payload: rawjson.Message(`{"query":"first"}`)},
				{Name: spec.Name, Payload: rawjson.Message(`{"query":"second"}`)},
			}}, nil
		}},
	}))
	_, err := createSessionForTest(t.Context(), store, "session")
	require.NoError(t, err)
	first, err := rt.MustClient(definition.route.ID).Run(t.Context(), "session", nil, WithRunID("source"), WithTurnID("turn-1"))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.Len(t, first.Suspension.Pending, 2)
	done := make(chan error, 1)
	go func() {
		_, err := rt.MustClient(definition.route.ID).Continue(t.Context(), "session", "source", "answer", "turn-2", &api.PendingInputResponse{
			MCP: &api.MCPInputResponse{ToolCallID: first.Suspension.Pending[0].MCP.ToolCallID, Responses: map[string]json.RawMessage{
				"": json.RawMessage(`{"action":"accept","content":{}}`),
			}},
		}, WorkflowOptions{})
		done <- err
	}()
	select {
	case <-updateStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Task answer did not start")
	}
	target := "answer"
	if source {
		target = "source"
	}
	require.NoError(t, rt.CancelRun(t.Context(), CancelRequest{RunID: target, Reason: run.CancellationReasonUserRequested}))
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(10 * time.Second):
		t.Fatal("canceled continuation did not settle")
	}
	assert.EqualValues(t, 2, creates.Load())
	assert.EqualValues(t, 1, updates.Load())
	assert.EqualValues(t, 2, cancels.Load())
	meta, err := store.LoadRun(t.Context(), "answer")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, meta.Status)
	assert.Equal(t, run.CancellationReasonUserRequested, meta.CancellationReason)
}

func TestPermanentTaskCleanupFailureClosesRunAsFailed(t *testing.T) {
	for _, cleanupError := range []error{errors.New("server rejected cancellation"), context.Canceled} {
		t.Run(cleanupError.Error(), func(t *testing.T) {
			rt := New(newTestStore(), WithEngine(engineinmem.New()))
			spec := newAnyJSONSpec("remote.tools.lookup")
			definition := NewAgentDefinition(AgentRoute{ID: "test.agent", WorkflowName: "test.workflow", DefaultTaskQueue: "test.queue"},
				[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, nil)
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
				Name: "remote.tools", Specs: []tools.ToolSpec{spec}, DecodeInExecutor: true,
				Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					if call.ExecutionContinuation != nil {
						return nil, engine.MarkActivityErrorNonRetryable(cleanupError)
					}
					pending, err := tooloperation.NewPendingTaskInput("task", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
						"": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
					}})
					if err != nil {
						return nil, err
					}
					return Unfinished(pending)
				},
			}))
			require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
				Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "plan", ExecuteToolActivity: "execute", ResumeActivityName: "resume",
				Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: spec.Name, Payload: rawjson.Message(`{}`)}}}, nil
				}},
			}))
			_, err := createSessionForTest(t.Context(), rt.Store, "session")
			require.NoError(t, err)
			first, err := rt.MustClient(definition.route.ID).Run(t.Context(), "session", nil, WithRunID("source"), WithTurnID("turn-1"))
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			_, err = rt.Store.(interface {
				EndSession(context.Context, string, time.Time) (session.Session, error)
			}).EndSession(t.Context(), "session", time.Now())
			require.NoError(t, err)
			_, err = rt.MustClient(definition.route.ID).Continue(t.Context(), "session", "source", "cleanup", "turn-2", &api.PendingInputResponse{
				MCP: &api.MCPInputResponse{ToolCallID: first.Suspension.Pending[0].MCP.ToolCallID, Responses: map[string]json.RawMessage{"": json.RawMessage(`{"action":"accept","content":{}}`)}},
			}, WorkflowOptions{})
			require.ErrorContains(t, err, cleanupError.Error())
			meta, err := rt.Store.LoadRun(t.Context(), "cleanup")
			require.NoError(t, err)
			assert.Equal(t, session.RunStatusFailed, meta.Status)
			assert.Equal(t, run.CancellationReasonSessionEnded, meta.CancellationReason)
			completion, err := rt.Engine.QueryRunCompletion(t.Context(), "cleanup")
			require.NoError(t, err)
			assert.Equal(t, engine.RunStatusFailed, completion.Status)
			assert.ErrorContains(t, completion.WorkflowError, cleanupError.Error())
		})
	}
}
