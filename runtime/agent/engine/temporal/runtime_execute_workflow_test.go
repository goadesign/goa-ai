package temporal

// runtime_execute_workflow_test.go verifies that a user-input request closes a
// real Temporal workflow with a continuation checkpoint instead of waiting for
// a second workflow.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

const runSuspensionType = "runtime.run_suspension"

func TestExecuteWorkflowSuspendsAwaitQuestions(t *testing.T) {
	t.Parallel()

	const (
		workflowName        = "service.workflow"
		taskQueue           = "service.queue"
		planActivityName    = "service.plan"
		resumeActivityName  = "service.resume"
		executeActivityName = "service.execute"
		turnID              = "turn-1"
		runID               = "run-await-questions"
		sessionID           = "session-1"
		awaitID             = "await-1"
		toolCallID          = "tool-call-1"
	)

	agentID := agent.Ident("service.agent")
	questionTool := tools.Ident("chat.ask_question.ask_question")
	plannerStub := &awaitQuestionsPlanner{awaitID: awaitID, toolName: questionTool, toolCallID: toolCallID}
	runtime := agentruntime.New()
	_, err := runtime.CreateSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(context.Background(), agentruntime.AgentRegistration{
		ID:      agentID,
		Planner: plannerStub,
		Workflow: engine.WorkflowDefinition{
			Name:      workflowName,
			TaskQueue: taskQueue,
			Handler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
				return runtime.ExecuteWorkflow(wfCtx, input)
			},
		},
		PlanActivityName:    planActivityName,
		ResumeActivityName:  resumeActivityName,
		ExecuteToolActivity: executeActivityName,
		Specs:               []tools.ToolSpec{anyJSONToolSpec(questionTool, "chat.await")},
	}))

	recorder := &hookRecorder{}
	eng := &Engine{defaultQueue: taskQueue, activityOptions: make(map[string]engine.ActivityOptions)}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(recorder.Record, activity.RegisterOptions{Name: "runtime.record_event"})
	env.RegisterActivityWithOptions(runtime.PlanStartActivity, activity.RegisterOptions{Name: planActivityName})
	env.RegisterActivityWithOptions(runtime.PlanResumeActivity, activity.RegisterOptions{Name: resumeActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) (*api.RunOutput, error) {
		return runtime.ExecuteWorkflow(NewWorkflowContext(eng, ctx), &agentruntime.RunInput{
			AgentID: agentID, RunID: runID, SessionID: sessionID, TurnID: turnID,
		})
	})

	require.NoError(t, env.GetWorkflowError())
	var out *api.RunOutput
	require.NoError(t, env.GetWorkflowResult(&out))
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
	require.Len(t, out.Suspension.Pending, 1)
	require.Equal(t, api.PendingInputKindToolResults, out.Suspension.Pending[0].Kind)
	require.True(t, recorder.SuspensionPersisted())
	require.Zero(t, plannerStub.ResumeCalls())

	events := recorder.Snapshot()
	require.NotEmpty(t, events)
	var awaitEvent *hooks.AwaitQuestionsEvent
	for _, event := range events {
		switch event := event.(type) {
		case *hooks.AwaitQuestionsEvent:
			awaitEvent = event
		case *hooks.RunCompletedEvent:
			require.Fail(t, "suspended workflow emitted RunCompleted")
		}
	}
	require.NotNil(t, awaitEvent)
	require.Equal(t, awaitID, awaitEvent.ID)
	require.Equal(t, questionTool, awaitEvent.ToolName)
	require.Equal(t, toolCallID, awaitEvent.ToolCallID)
	suspended, ok := events[len(events)-1].(*hooks.RunSuspendedEvent)
	require.True(t, ok)
	require.Equal(t, out.Suspension.ID, suspended.SuspensionID)
}

type awaitQuestionsPlanner struct {
	mu          sync.Mutex
	resumeCalls int
	awaitID     string
	toolName    tools.Ident
	toolCallID  string
}

func (p *awaitQuestionsPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	title := "Questions"
	return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitQuestionsItem(&planner.AwaitQuestions{
		ID: p.awaitID, ToolName: p.toolName, ToolCallID: p.toolCallID,
		Payload: rawjson.Message(`{"title":"Questions"}`), Title: &title,
		Questions: []planner.AwaitQuestion{{
			ID: "q1", Prompt: "Choose one answer",
			Options: []planner.AwaitQuestionOption{{ID: "yes", Label: "Yes"}, {ID: "no", Label: "No"}},
		}},
	}))}, nil
}

func (p *awaitQuestionsPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.mu.Lock()
	p.resumeCalls++
	p.mu.Unlock()
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "resumed"}},
	}}}, nil
}

func (p *awaitQuestionsPlanner) ResumeCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resumeCalls
}

type hookRecorder struct {
	mu                  sync.Mutex
	events              []hooks.Event
	suspensionPersisted bool
}

func (r *hookRecorder) Record(_ context.Context, input *api.RecordActivityInput) error {
	if input.Type == transcript.RunLogMessagesSeeded || input.Type == transcript.RunLogMessagesAppended {
		return nil
	}
	if input.Type == runSuspensionType {
		r.mu.Lock()
		r.suspensionPersisted = true
		r.mu.Unlock()
		return nil
	}
	event, err := hooks.DecodeFromRecordInput(input)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func (r *hookRecorder) Snapshot() []hooks.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hooks.Event(nil), r.events...)
}

func (r *hookRecorder) SuspensionPersisted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.suspensionPersisted
}

func anyJSONToolSpec(name tools.Ident, toolset string) tools.ToolSpec {
	codec := tools.JSONCodec[any]{
		ToJSON: json.Marshal,
		FromJSON: func(data []byte) (any, error) {
			var out any
			if err := json.Unmarshal(data, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	}
	return tools.ToolSpec{
		Name: name, Toolset: toolset,
		Payload: tools.TypeSpec{Name: string(name) + "_payload", Codec: codec},
		Result:  tools.TypeSpec{Name: string(name) + "_result", Codec: codec},
	}
}
