package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

const (
	testWorkflowID = "wf"
	testRunID      = "run"
	testSessionID  = "sess"
)

type panicWorkflowContext struct{}

func (panicWorkflowContext) Context() context.Context {
	return context.Background()
}

func (panicWorkflowContext) SetQueryHandler(name string, handler any) error {
	return nil
}

func (panicWorkflowContext) WorkflowID() string {
	return testWorkflowID
}

func (panicWorkflowContext) RunID() string {
	return testRunID
}

func (panicWorkflowContext) Detached() engine.WorkflowContext {
	return panicWorkflowContext{}
}

func (panicWorkflowContext) PublishHook(ctx context.Context, call engine.HookActivityCall) error {
	panic("unexpected PublishHook from activity context")
}

func (panicWorkflowContext) ExecutePlannerActivity(ctx context.Context, call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	return nil, nil
}

func (panicWorkflowContext) ExecuteToolActivity(ctx context.Context, call engine.ToolActivityCall) (*api.ToolOutput, error) {
	return nil, nil
}

func (panicWorkflowContext) ExecuteToolActivityAsync(ctx context.Context, call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	return nil, nil
}

func (panicWorkflowContext) PauseRequests() engine.Receiver[api.PauseRequest] {
	return nil
}

func (panicWorkflowContext) ResumeRequests() engine.Receiver[api.ResumeRequest] {
	return nil
}

func (panicWorkflowContext) ClarificationAnswers() engine.Receiver[api.ClarificationAnswer] {
	return nil
}

func (panicWorkflowContext) ExternalToolResults() engine.Receiver[api.ToolResultsSet] {
	return nil
}

func (panicWorkflowContext) ConfirmationDecisions() engine.Receiver[api.ConfirmationDecision] {
	return nil
}

func (panicWorkflowContext) Now() time.Time {
	return time.Unix(0, 0).UTC()
}

func (panicWorkflowContext) NewTimer(ctx context.Context, d time.Duration) (engine.Future[time.Time], error) {
	return nil, nil
}

func (panicWorkflowContext) Await(ctx context.Context, condition func() bool) error {
	return nil
}

func (panicWorkflowContext) StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	return nil, nil
}

func (panicWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	return panicWorkflowContext{}, func() {}
}

func TestPublishHookErr_DoesNotUseWorkflowContextFromActivity(t *testing.T) {
	rt := &Runtime{
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
	}

	ctx := engine.WithActivityContext(engine.WithWorkflowContext(context.Background(), panicWorkflowContext{}))
	err := rt.publishHookErr(ctx, hooks.NewPlannerNoteEvent(testRunID, agent.Ident("agent"), testSessionID, "note", nil), "turn-1")
	require.NoError(t, err)
}

type failingRunlogStore struct {
	err error
}

func (s failingRunlogStore) Append(_ context.Context, _ *runlog.Event) error {
	return s.err
}

func (s failingRunlogStore) List(_ context.Context, _ string, _ string, _ int) (runlog.Page, error) {
	return runlog.Page{}, s.err
}

func TestPublishHook_ReturnsError(t *testing.T) {
	sentinel := errors.New("append failed")
	rt := &Runtime{
		RunEventStore: failingRunlogStore{err: sentinel},
		Bus:           noopHooks{},
		logger:        telemetry.NoopLogger{},
		tracer:        telemetry.NewNoopTracer(),
	}

	require.NotPanics(t, func() {
		err := rt.publishHook(context.Background(), hooks.NewPlannerNoteEvent(testRunID, agent.Ident("agent"), testSessionID, "note", nil), "turn-1")
		require.ErrorIs(t, err, sentinel)
	})
}

type cancelOnPlannerState struct {
	detachedCalled bool
	terminalSent   bool
}

type cancelOnPlannerWorkflowContext struct {
	ctx    context.Context
	cancel context.CancelFunc
	state  *cancelOnPlannerState
}

func newCancelOnPlannerWorkflowContext() *cancelOnPlannerWorkflowContext {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelOnPlannerWorkflowContext{
		ctx:    ctx,
		cancel: cancel,
		state:  &cancelOnPlannerState{},
	}
}

func (w *cancelOnPlannerWorkflowContext) Context() context.Context {
	return engine.WithWorkflowContext(w.ctx, w)
}

func (w *cancelOnPlannerWorkflowContext) SetQueryHandler(name string, handler any) error {
	return nil
}

func (w *cancelOnPlannerWorkflowContext) WorkflowID() string {
	return testWorkflowID
}

func (w *cancelOnPlannerWorkflowContext) RunID() string {
	return testRunID
}

func (w *cancelOnPlannerWorkflowContext) PublishHook(ctx context.Context, call engine.HookActivityCall) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Record only successful terminal hook emission attempts.
	if call.Input != nil && call.Input.Type == hooks.RunCompleted {
		w.state.terminalSent = true
	}
	return nil
}

func (w *cancelOnPlannerWorkflowContext) ExecutePlannerActivity(ctx context.Context, call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	w.cancel()
	return nil, context.Canceled
}

func (w *cancelOnPlannerWorkflowContext) ExecuteToolActivity(ctx context.Context, call engine.ToolActivityCall) (*api.ToolOutput, error) {
	return nil, errors.New("unexpected tool activity")
}

func (w *cancelOnPlannerWorkflowContext) ExecuteToolActivityAsync(ctx context.Context, call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	return nil, errors.New("unexpected tool activity")
}

func (w *cancelOnPlannerWorkflowContext) PauseRequests() engine.Receiver[api.PauseRequest] {
	return nil
}

func (w *cancelOnPlannerWorkflowContext) ResumeRequests() engine.Receiver[api.ResumeRequest] {
	return nil
}

func (w *cancelOnPlannerWorkflowContext) ClarificationAnswers() engine.Receiver[api.ClarificationAnswer] {
	return nil
}

func (w *cancelOnPlannerWorkflowContext) ExternalToolResults() engine.Receiver[api.ToolResultsSet] {
	return nil
}

func (w *cancelOnPlannerWorkflowContext) ConfirmationDecisions() engine.Receiver[api.ConfirmationDecision] {
	return nil
}

func (w *cancelOnPlannerWorkflowContext) Now() time.Time {
	return time.Unix(0, 0).UTC()
}

func (w *cancelOnPlannerWorkflowContext) NewTimer(ctx context.Context, d time.Duration) (engine.Future[time.Time], error) {
	return nil, errors.New("unexpected timer")
}

func (w *cancelOnPlannerWorkflowContext) Await(ctx context.Context, condition func() bool) error {
	return errors.New("unexpected await")
}

func (w *cancelOnPlannerWorkflowContext) StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	return nil, errors.New("unexpected child workflow")
}

func (w *cancelOnPlannerWorkflowContext) Detached() engine.WorkflowContext {
	w.state.detachedCalled = true
	return &cancelOnPlannerWorkflowContext{
		ctx:    context.Background(),
		cancel: func() {},
		state:  w.state,
	}
}

func (w *cancelOnPlannerWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	return w, func() {}
}

func TestExecuteWorkflow_TerminalHookUsesDetachedContext(t *testing.T) {
	wfCtx := newCancelOnPlannerWorkflowContext()
	rt := &Runtime{
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
		logger:        telemetry.NoopLogger{},
		tracer:        telemetry.NewNoopTracer(),
		agents: map[agent.Ident]AgentRegistration{
			agent.Ident("agent"): {
				ID:               agent.Ident("agent"),
				PlanActivityName: "plan",
			},
		},
	}

	_, err := rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   agent.Ident("agent"),
		RunID:     testRunID,
		SessionID: testSessionID,
		TurnID:    "turn-1",
	})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, wfCtx.state.detachedCalled, "expected terminal emission to use Detached workflow context")
	require.True(t, wfCtx.state.terminalSent, "expected RunCompleted hook emission to succeed via Detached context")
}
