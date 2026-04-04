package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type ledgerQueryBeforeFirstPlanWorkflowContext struct {
	ctx context.Context

	pauseCh   chan *api.PauseRequest
	resumeCh  chan *api.ResumeRequest
	clarifyCh chan *api.ClarificationAnswer
	resultsCh chan *api.ToolResultsSet
	confirmCh chan *api.ConfirmationDecision

	queryName    string
	queryHandler func() ([]*model.Message, error)

	queryBeforeFirstPlan []*model.Message
}

func newLedgerQueryBeforeFirstPlanWorkflowContext() *ledgerQueryBeforeFirstPlanWorkflowContext {
	return &ledgerQueryBeforeFirstPlanWorkflowContext{
		ctx:       context.Background(),
		pauseCh:   make(chan *api.PauseRequest, 1),
		resumeCh:  make(chan *api.ResumeRequest, 1),
		clarifyCh: make(chan *api.ClarificationAnswer, 1),
		resultsCh: make(chan *api.ToolResultsSet, 1),
		confirmCh: make(chan *api.ConfirmationDecision, 1),
	}
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) Context() context.Context {
	return engine.WithWorkflowContext(w.ctx, w)
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) SetQueryHandler(name string, handler any) error {
	queryHandler, ok := handler.(func() ([]*model.Message, error))
	if !ok {
		return fmt.Errorf("unexpected query handler type %T", handler)
	}
	w.queryName = name
	w.queryHandler = queryHandler
	return nil
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) WorkflowID() string {
	return testWorkflowID
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) RunID() string {
	return testRunID
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) PublishHook(context.Context, engine.HookActivityCall) error {
	return nil
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) ExecutePlannerActivity(context.Context, engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	if w.queryName != transcript.QueryLedgerMessages || w.queryHandler == nil {
		return nil, errors.New("ledger query handler not registered before first plan activity")
	}
	msgs, err := w.queryHandler()
	if err != nil {
		return nil, err
	}
	w.queryBeforeFirstPlan = append([]*model.Message(nil), msgs...)
	return &api.PlanActivityOutput{
		Result: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{
						model.TextPart{Text: "done"},
					},
				},
			},
		},
		Transcript: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: "done"},
				},
			},
		},
	}, nil
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) ExecuteToolActivity(context.Context, engine.ToolActivityCall) (*api.ToolOutput, error) {
	return nil, errors.New("unexpected tool activity")
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) ExecuteToolActivityAsync(context.Context, engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	return nil, errors.New("unexpected async tool activity")
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) PauseRequests() engine.Receiver[*api.PauseRequest] {
	return testReceiver[*api.PauseRequest]{ch: w.pauseCh}
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) ResumeRequests() engine.Receiver[*api.ResumeRequest] {
	return testReceiver[*api.ResumeRequest]{ch: w.resumeCh}
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) ClarificationAnswers() engine.Receiver[*api.ClarificationAnswer] {
	return testReceiver[*api.ClarificationAnswer]{ch: w.clarifyCh}
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) ExternalToolResults() engine.Receiver[*api.ToolResultsSet] {
	return testReceiver[*api.ToolResultsSet]{ch: w.resultsCh}
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) ConfirmationDecisions() engine.Receiver[*api.ConfirmationDecision] {
	return testReceiver[*api.ConfirmationDecision]{ch: w.confirmCh}
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) Now() time.Time {
	return time.Unix(0, 0).UTC()
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) NewTimer(context.Context, time.Duration) (engine.Future[time.Time], error) {
	return nil, errors.New("unexpected timer")
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) Await(context.Context, func() bool) error {
	return errors.New("unexpected await")
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) StartChildWorkflow(context.Context, engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	return nil, errors.New("unexpected child workflow")
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) Detached() engine.WorkflowContext {
	return w
}

func (w *ledgerQueryBeforeFirstPlanWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	return w, func() {}
}

func TestExecuteWorkflowRegistersLedgerQueryBeforeFirstPlanActivity(t *testing.T) {
	const agentID = agent.Ident("agent")

	wfCtx := newLedgerQueryBeforeFirstPlanWorkflowContext()
	rt := newTestRuntimeWithPlanner(agentID, nil)
	rt.agents[agentID] = AgentRegistration{
		ID:               agentID,
		PlanActivityName: "plan",
	}
	input := &RunInput{
		AgentID:   agentID,
		RunID:     testRunID,
		SessionID: testSessionID,
		TurnID:    "turn-1",
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, transcript.QueryLedgerMessages, wfCtx.queryName)
	require.Empty(t, wfCtx.queryBeforeFirstPlan)

	msgs, err := wfCtx.queryHandler()
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, model.ConversationRoleAssistant, msgs[0].Role)
	require.Equal(t, []model.Part{model.TextPart{Text: "done"}}, msgs[0].Parts)
}
