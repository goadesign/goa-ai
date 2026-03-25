package temporal

// runtime_execute_workflow_test.go exercises Runtime.ExecuteWorkflow on top of
// Temporal's workflow test suite so await-question cancellation is verified
// through the real Temporal signal receiver.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

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
	"goa.design/goa-ai/runtime/agent/run"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestExecuteWorkflowCancelsAwaitQuestionsBeforeLateResults(t *testing.T) {
	t.Parallel()

	const (
		workflowName        = "service.workflow"
		taskQueue           = "service.queue"
		planActivityName    = "service.plan"
		resumeActivityName  = "service.resume"
		executeActivityName = "service.execute"
		turnID              = "turn-1"
		runID               = "run-await-questions-cancel"
		sessionID           = "session-1"
		awaitID             = "await-1"
		questionToolCallID  = "tool-call-1"
	)

	agentID := agent.Ident("service.agent")
	questionTool := tools.Ident("chat.ask_question.ask_question")

	pl := &awaitQuestionsPlanner{
		awaitID:    awaitID,
		toolName:   questionTool,
		toolCallID: questionToolCallID,
	}
	rt := agentruntime.New()
	require.NoError(t, rt.RegisterAgent(context.Background(), agentruntime.AgentRegistration{
		ID:      agentID,
		Planner: pl,
		Workflow: engine.WorkflowDefinition{
			Name:      workflowName,
			TaskQueue: taskQueue,
			Handler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
				return rt.ExecuteWorkflow(wfCtx, input)
			},
		},
		PlanActivityName:    planActivityName,
		ResumeActivityName:  resumeActivityName,
		ExecuteToolActivity: executeActivityName,
		Specs: []tools.ToolSpec{
			anyJSONToolSpec(questionTool, "chat.await"),
		},
	}))

	recorder := &hookRecorder{}
	eng := &Engine{
		defaultQueue:    taskQueue,
		activityOptions: make(map[string]engine.ActivityOptions),
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(recorder.Record, activity.RegisterOptions{Name: "runtime.publish_hook"})
	env.RegisterActivityWithOptions(rt.PlanStartActivity, activity.RegisterOptions{Name: planActivityName})
	env.RegisterActivityWithOptions(rt.PlanResumeActivity, activity.RegisterOptions{Name: resumeActivityName})

	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(api.SignalProvideToolResults, &api.ToolResultsSet{
			RunID: runID,
			ID:    awaitID,
			Results: []*api.ProvidedToolResult{
				{
					Name:       questionTool,
					ToolCallID: questionToolCallID,
					Result:     rawjson.Message([]byte(`{"answers":[{"question_id":"q1","selected_option_ids":["yes"]}]}`)),
				},
			},
		})
	}, 2*time.Second)

	env.ExecuteWorkflow(func(ctx workflow.Context) (*api.RunOutput, error) {
		return rt.ExecuteWorkflow(NewWorkflowContext(eng, ctx), &agentruntime.RunInput{
			AgentID:   agentID,
			RunID:     runID,
			SessionID: sessionID,
			TurnID:    turnID,
		})
	})

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.ErrorContains(t, err, "canceled")
	require.Zero(t, pl.ResumeCalls(), "cancel should win before late tool results resume the run")

	events := recorder.Snapshot()
	require.NotEmpty(t, events)

	var (
		awaitEvent     *hooks.AwaitQuestionsEvent
		completedEvent *hooks.RunCompletedEvent
		sawPaused      bool
		sawResumed     bool
		sawToolResult  bool
	)
	for _, evt := range events {
		switch e := evt.(type) {
		case *hooks.AwaitQuestionsEvent:
			awaitEvent = e
		case *hooks.RunPausedEvent:
			sawPaused = true
		case *hooks.RunResumedEvent:
			sawResumed = true
		case *hooks.ToolResultReceivedEvent:
			sawToolResult = true
		case *hooks.RunCompletedEvent:
			completedEvent = e
		}
	}

	require.NotNil(t, awaitEvent, "expected await_questions event before cancellation")
	require.Equal(t, awaitID, awaitEvent.ID)
	require.Equal(t, questionTool, awaitEvent.ToolName)
	require.Equal(t, questionToolCallID, awaitEvent.ToolCallID)
	require.True(t, sawPaused, "expected run to pause while awaiting user answers")
	require.False(t, sawResumed, "canceled await should not emit a resume event")
	require.False(t, sawToolResult, "late tool results should not be consumed after cancellation")

	require.NotNil(t, completedEvent, "expected terminal run completion event")
	require.Equal(t, "canceled", completedEvent.Status)
	require.Equal(t, run.PhaseCanceled, completedEvent.Phase)
	lastEvent, ok := events[len(events)-1].(*hooks.RunCompletedEvent)
	require.True(t, ok, "terminal completion should be the final hook")
	require.Equal(t, "canceled", lastEvent.Status)
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
	return &planner.PlanResult{
		Await: planner.NewAwait(planner.AwaitQuestionsItem(&planner.AwaitQuestions{
			ID:         p.awaitID,
			ToolName:   p.toolName,
			ToolCallID: p.toolCallID,
			Payload:    rawjson.Message([]byte(`{"title":"Questions"}`)),
			Title:      &title,
			Questions: []planner.AwaitQuestion{
				{
					ID:     "q1",
					Prompt: "Choose one answer",
					Options: []planner.AwaitQuestionOption{
						{ID: "yes", Label: "Yes"},
						{ID: "no", Label: "No"},
					},
				},
			},
		})),
	}, nil
}

func (p *awaitQuestionsPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.mu.Lock()
	p.resumeCalls++
	p.mu.Unlock()
	return &planner.PlanResult{
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: "unexpected resume"},
				},
			},
		},
	}, nil
}

// ResumeCalls reports how many times the workflow resumed after the await barrier.
func (p *awaitQuestionsPlanner) ResumeCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resumeCalls
}

type hookRecorder struct {
	mu     sync.Mutex
	events []hooks.Event
}

// Record decodes the hook activity payload so the test can assert on emitted events.
func (r *hookRecorder) Record(_ context.Context, input *api.HookActivityInput) error {
	evt, err := hooks.DecodeFromHookInput(input)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.events = append(r.events, evt)
	r.mu.Unlock()
	return nil
}

// Snapshot returns a stable copy of the recorded hook sequence.
func (r *hookRecorder) Snapshot() []hooks.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]hooks.Event, len(r.events))
	copy(out, r.events)
	return out
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
		Name:    name,
		Toolset: toolset,
		Payload: tools.TypeSpec{Name: string(name) + "_payload", Codec: codec},
		Result:  tools.TypeSpec{Name: string(name) + "_result", Codec: codec},
	}
}
