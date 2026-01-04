package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
)

type panicWorkflowContext struct{}

func (panicWorkflowContext) Context() context.Context {
	return context.Background()
}

func (panicWorkflowContext) SetQueryHandler(name string, handler any) error {
	return nil
}

func (panicWorkflowContext) WorkflowID() string {
	return "wf"
}

func (panicWorkflowContext) RunID() string {
	return "run"
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
	err := rt.publishHookErr(ctx, hooks.NewPlannerNoteEvent("run", agent.Ident("agent"), "sess", "note", nil), "turn-1")
	require.NoError(t, err)
}


