//nolint:lll // allow long lines in test literals for readability
package runtime

// This file checks runtime registration, startup, planner execution, and
// engine integration contracts shared by generated agents.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type forgedRuntimeClient struct {
	model.Client
}

func TestRegisterModelRejectsTypedNilClient(t *testing.T) {
	var client *forgedRuntimeClient

	err := New(newTestStore()).RegisterModel("primary", client)

	require.EqualError(t, err, `register model "primary": model client is required`)
}

// nestedPlannerStub discovers children across iterations: first 2 children,
// then 1, then final.
type nestedPlannerStub struct {
	iter int
}

var _ engine.WorkflowContext = (*testWorkflowContext)(nil)
var _ engine.Future[*api.ToolOutput] = (*testToolFuture)(nil)

func (p *nestedPlannerStub) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	p.iter = 0
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: tools.Ident("child1"), Payload: rawjson.Message(`{}`)},
		{Name: tools.Ident("child2"), Payload: rawjson.Message(`{}`)},
	}}, nil
}
func (p *nestedPlannerStub) PlanResume(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.iter++
	if p.iter == 1 {
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name:    tools.Ident("child3"),
			Payload: rawjson.Message(`{}`),
		}}}, nil
	}
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "nested done"}}}}}, nil
}
func TestStartRunSetsWorkflowName(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				Definition: testAgentDefinition("service.agent", "service.workflow", "svc.queue", nil, nil),
			},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	sess, err := createSessionForTest(context.Background(), rt.Store, "sess-1")
	require.NoError(t, err)
	require.Equal(t, "sess-1", sess.ID)
	require.Equal(t, session.StatusActive, sess.Status)
	_, err = client.Start(context.Background(), "sess-1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.last.Workflow)
}

func TestStartRunRequiresSessionID(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, nil)},
		},
	}
	// Empty session ID
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := client.Start(context.Background(), "", nil, WithRunID("run-1"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingSessionID)
	// Whitespace session ID
	_, err = client.Start(context.Background(), "  \t  ", nil, WithRunID("run-1"))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrMissingSessionID)
	// Valid session ID
	_, err = createSessionForTest(context.Background(), rt.Store, "s1")
	require.NoError(t, err)
	_, err = client.Start(context.Background(), "s1", nil, WithRunID("run-1"))
	require.NoError(t, err)
}

func TestStartRunDoesNotInjectSessionSearchAttribute(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, nil)},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := createSessionForTest(context.Background(), rt.Store, "sess-1")
	require.NoError(t, err)
	_, err = client.Start(context.Background(), "sess-1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	require.Nil(t, eng.last.SearchAttributes)
}

func TestFinishCurrentPlanResult_UsesPlannerFinalToolResult(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		Bus:     noopHooks{},
	}
	input := &RunInput{
		AgentID:   "svc.agent",
		SessionID: "sess-1",
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			Tool:      "svc.tools.do",
		},
	}
	st := &runLoopState{
		ResponseCommitted: true,
		Result: &PlanResult{
			FinalToolResult: &planner.FinalToolResult{
				Result: rawjson.Message([]byte(`{"status":"ok"}`)),
			},
		},
	}

	out, err := rt.finishCurrentPlanResult(context.Background(), input, base, st, "turn-1")
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.FinalToolResult)
	require.JSONEq(t, `{"status":"ok"}`, string(out.FinalToolResult.Result))
}

func TestRunLoopWithStateAcceptsInitialFinalToolResult(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		Bus:     noopHooks{},
	}
	seedTestToolSpecs(rt, newAnyJSONSpec("svc.tools.do"))
	wf := &testWorkflowContext{ctx: context.Background()}
	input := &RunInput{
		AgentID:   "svc.agent",
		SessionID: "sess-1",
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			Tool:      "svc.tools.do",
		},
	}
	st := &runLoopState{
		Caps: initialCaps(RunPolicy{}),
		Result: &PlanResult{
			FinalToolResult: &planner.FinalToolResult{
				Result: rawjson.Message([]byte(`{"status":"ok"}`)),
			},
		},
	}

	out, err := rt.runLoopWithState(
		wf,
		AgentRegistration{},
		input,
		base,
		st,
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.FinalToolResult)
	require.JSONEq(t, `{"status":"ok"}`, string(out.FinalToolResult.Result))
}

func TestFinishCurrentPlanResultRejectsDualTerminalOutputs(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		Bus:     noopHooks{},
	}
	input := &RunInput{
		AgentID:   "svc.agent",
		SessionID: "sess-1",
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			Tool:      "svc.tools.do",
		},
	}
	st := &runLoopState{
		ResponseCommitted: true,
		Result: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done"}},
				},
			},
			FinalToolResult: &planner.FinalToolResult{
				Result: rawjson.Message([]byte(`{"status":"ok"}`)),
			},
		},
	}

	out, err := rt.finishCurrentPlanResult(context.Background(), input, base, st, "turn-1")
	require.Error(t, err)
	require.Nil(t, out)
	require.Contains(t, err.Error(), "both")
}

func TestFinishCurrentPlanResultAppendsTerminalTranscript(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore()
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   store,
	}
	input := &RunInput{
		AgentID:   "svc.agent",
		SessionID: "sess-1",
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
		},
	}
	st := &runLoopState{
		Result: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done"}},
				},
			},
		},
		Transcript: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ThinkingPart{
					Text:      "reasoning",
					Signature: "sig",
					Final:     true,
				},
				model.TextPart{Text: "done"},
			},
		}},
	}
	require.NoError(t, rt.appendSelectedModelResponse(ctx, input.AgentID, base, "turn-1", st.Result, st.Transcript))
	st.ResponseCommitted = true

	out, err := rt.finishCurrentPlanResult(ctx, input, base, st, "turn-1")
	require.NoError(t, err)
	require.Equal(t, "done", agentMessageText(out.Final))
	require.Equal(t, []model.Part{model.TextPart{Text: "done"}}, out.Final.Parts)

	page, err := store.ListRunRecords(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, transcript.RunLogMessagesAppended, page.Events[1].Type)

	msgs, err := transcript.DecodeRunLogDelta(page.Events[1].Payload)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "done", agentMessageText(msgs[0]))
	require.IsType(t, model.ThinkingPart{}, msgs[0].Parts[0])
}

func TestFinishCurrentPlanResultAppendsTerminalTranscriptFromCitationsPart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore()
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   store,
	}
	input := &RunInput{
		AgentID:   "svc.agent",
		SessionID: "sess-1",
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
		},
	}
	st := &runLoopState{
		Result: &PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.CitationsPart{
						Text: "cited answer",
					}},
				},
			},
		},
	}
	require.NoError(t, rt.appendSelectedModelResponse(ctx, input.AgentID, base, "turn-1", st.Result, st.Transcript))
	st.ResponseCommitted = true

	out, err := rt.finishCurrentPlanResult(ctx, input, base, st, "turn-1")
	require.NoError(t, err)
	require.Equal(t, "cited answer", agentMessageText(out.Final))

	page, err := store.ListRunRecords(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, transcript.RunLogMessagesAppended, page.Events[1].Type)

	msgs, err := transcript.DecodeRunLogDelta(page.Events[1].Payload)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	require.Equal(t, "cited answer", agentMessageText(msgs[0]))
}

func TestExecuteWorkflowSeedsInitialTranscriptInsteadOfAppendingHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore()
	sessions := store
	_, err := sessions.CreateSession(ctx, "sess-1", time.Now().UTC())
	require.NoError(t, err)
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   store,
		Bus:     noopHooks{},
		agents: map[agent.Ident]AgentRegistration{
			"svc.agent": {
				Definition: testAgentDefinition("svc.agent", "svc.agent.workflow", "test", nil, nil),
				Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
					require.Equal(t, "state-json", input.RunContext.Metadata["task_state"])
					return &planner.PlanResult{
						FinalResponse: &planner.FinalResponse{
							Message: &model.Message{
								Role:  model.ConversationRoleAssistant,
								Parts: []model.Part{model.TextPart{Text: "done"}},
							},
						},
					}, nil
				}},
				PlanActivityName: "plan",
			},
		},
	}
	wfCtx := &testWorkflowContext{
		ctx:         ctx,
		runtime:     rt,
		hookRuntime: rt,
	}
	_, err = rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "svc.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Metadata:  map[string]any{"task_state": "state-json"},
		Messages: []*model.Message{
			{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: "prior user"}},
			},
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "prior assistant"}},
			},
		},
	})
	require.NoError(t, err)
	storedRun, err := sessions.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	require.Empty(t, storedRun.CancellationReason)

	page, err := store.ListRunRecords(ctx, "run-1", "", 20)
	require.NoError(t, err)
	var transcriptEvents []*runlog.Event
	for _, event := range page.Events {
		if event.Type == transcript.RunLogMessagesSeeded || event.Type == transcript.RunLogMessagesAppended {
			transcriptEvents = append(transcriptEvents, event)
		}
	}
	require.Len(t, transcriptEvents, 2)
	require.Equal(t, transcript.RunLogMessagesSeeded, transcriptEvents[0].Type)
	require.Equal(t, transcript.RunLogMessagesAppended, transcriptEvents[1].Type)

	seeded, err := transcript.DecodeRunLogDelta(transcriptEvents[0].Payload)
	require.NoError(t, err)
	require.Len(t, seeded, 2)
	require.Equal(t, "prior assistant", agentMessageText(seeded[1]))

	appended, err := transcript.DecodeRunLogDelta(transcriptEvents[1].Payload)
	require.NoError(t, err)
	require.Len(t, appended, 1)
	require.Equal(t, "done", agentMessageText(appended[0]))
}

func TestExecuteWorkflowSeedsRestoredContinuationTranscript(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore()
	sessions := store
	_, err := sessions.CreateSession(ctx, "sess-1", time.Now().UTC())
	require.NoError(t, err)
	tool := newAnyJSONSpec(tools.Ident("assistant.ask_clarification"))
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   store,
		Bus:     noopHooks{},
	}
	seedTestToolSpecs(rt, tool)
	rt.agents = map[agent.Ident]AgentRegistration{
		"svc.agent": {
			Definition: testAgentDefinition("svc.agent", "svc.agent.workflow", "test", []tools.ToolSpec{tool}, nil),
			Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				require.NoError(t, transcript.ValidatePlannerTranscript(input.Messages))
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "done"}},
				}}}, nil
			}},
			ResumeActivityName: "resume",
		},
	}

	firstInput := &RunInput{
		AgentID:   "svc.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, firstInput)
	firstContext := &testWorkflowContext{
		ctx:         ctx,
		runtime:     rt,
		hookRuntime: rt,
	}
	first, err := rt.runLoop(
		firstContext,
		AgentRegistration{ResumeActivityName: "resume"},
		firstInput,
		&planner.PlanInput{RunContext: run.Context{
			RunID: "run-1", SessionID: "sess-1", TurnID: "turn-1", Attempt: 1,
		}},
		&PlanResult{Await: planner.NewAwait(
			planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
				ID:              "clarification-1",
				ToolName:        tool.Name,
				ModelToolCallID: "call-1",
				Payload:         rawjson.Message(`{"question":"Which facility?"}`),
				Question:        "Which facility?",
			}),
		)},
		initialCaps(RunPolicy{MaxToolCalls: 4}),
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	require.NoError(t, storeSuspensionForTest(ctx, store, firstInput.RunID, session.RunSuspension{
		ID: first.Suspension.ID, Data: []byte(`{}`),
	}))

	secondContext := &testWorkflowContext{
		ctx:         ctx,
		runtime:     rt,
		hookRuntime: rt,
	}
	out, err := rt.ExecuteWorkflow(secondContext, &RunInput{
		AgentID:   "svc.agent",
		RunID:     "run-2",
		SessionID: "sess-1",
		TurnID:    "turn-2",
		Continuation: &api.RunContinuationInput{
			Suspension: first.Suspension,
			Response: &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
				ID:     "clarification-1",
				Answer: "Building A",
			}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "done", agentMessageText(out.Final))

	page, err := store.ListRunRecords(ctx, "run-2", "", 20)
	require.NoError(t, err)
	var (
		transcriptEvents []*runlog.Event
		messages         []*model.Message
		started          *hooks.RunStartedEvent
	)
	for _, event := range page.Events {
		if event.Type == hooks.RunStarted {
			decoded, err := hooks.DecodeFromRecordInput(&RecordActivityInput{
				Type: event.Type, RunID: event.RunID, AgentID: event.AgentID,
				SessionID: event.SessionID, TurnID: event.TurnID,
				TimestampMS: event.Timestamp.UnixMilli(), Payload: event.Payload,
			})
			require.NoError(t, err)
			started = decoded.(*hooks.RunStartedEvent)
		}
		if event.Type != transcript.RunLogMessagesSeeded && event.Type != transcript.RunLogMessagesAppended {
			continue
		}
		transcriptEvents = append(transcriptEvents, event)
		delta, err := transcript.DecodeRunLogDelta(event.Payload)
		require.NoError(t, err)
		messages = append(messages, delta...)
	}
	require.NotEmpty(t, transcriptEvents)
	require.Equal(t, transcript.RunLogMessagesSeeded, transcriptEvents[0].Type)
	require.NoError(t, transcript.ValidatePlannerTranscript(messages))
	require.Len(t, messages, 3)
	require.Equal(t, model.ConversationRoleAssistant, messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, messages[1].Role)
	require.Equal(t, model.ConversationRoleAssistant, messages[2].Role)
	require.NotNil(t, started)
	require.Equal(t, "run-1", started.PredecessorRunID)
}

func TestExecuteWorkflowEmitsRunLabelsOnTerminalCompletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore()
	sessions := store
	_, err := sessions.CreateSession(ctx, "sess-1", time.Now().UTC())
	require.NoError(t, err)
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   store,
		Bus:     noopHooks{},
		agents: map[agent.Ident]AgentRegistration{
			"svc.agent": {
				Definition: testAgentDefinition("svc.agent", "svc.agent.workflow", "test", nil, nil),
				Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return &planner.PlanResult{
						FinalResponse: &planner.FinalResponse{
							Message: &model.Message{
								Role:  model.ConversationRoleAssistant,
								Parts: []model.Part{model.TextPart{Text: "done"}},
							},
						},
					}, nil
				}},
				PlanActivityName: "plan",
			},
		},
	}
	wfCtx := &testWorkflowContext{
		ctx:         ctx,
		runtime:     rt,
		hookRuntime: rt,
	}

	labels := map[string]string{"household_id": "house-42", "source": "email"}
	_, err = rt.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "svc.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Labels:    labels,
	})
	require.NoError(t, err)

	page, err := store.ListRunRecords(ctx, "run-1", "", 20)
	require.NoError(t, err)
	require.NotEmpty(t, page.Events)
	last := page.Events[len(page.Events)-1]
	require.Equal(t, hooks.RunCompleted, last.Type)

	decoded, err := hooks.DecodeFromRecordInput(&runlog.ActivityInput{
		Type:      last.Type,
		RunID:     last.RunID,
		AgentID:   last.AgentID,
		SessionID: last.SessionID,
		TurnID:    last.TurnID,
		Payload:   last.Payload,
	})
	require.NoError(t, err)
	completed, ok := decoded.(*hooks.RunCompletedEvent)
	require.True(t, ok)
	require.Equal(t, "success", completed.Status)
	require.Equal(t, labels, completed.Labels)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, labels, snapshot.Labels)
}

func TestStartOneShotDoesNotRequireSession(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, nil),
			},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	handle, err := client.StartOneShot(
		context.Background(),
		nil,
		WithRunID("run-oneshot-1"),
		WithTurnID("turn-oneshot-1"),
	)
	require.NoError(t, err)
	require.NotNil(t, handle)
	require.Equal(t, "service.workflow", eng.last.Workflow)
	require.Equal(t, "run-oneshot-1", eng.last.ID)
	in := eng.last.Input
	require.Equal(t, "run-oneshot-1", in.RunID)
	require.Equal(t, "turn-oneshot-1", in.TurnID)
	require.Empty(t, in.SessionID)
	if eng.last.SearchAttributes != nil {
		_, ok := eng.last.SearchAttributes["SessionID"]
		require.False(t, ok)
	}
}

func TestOneShotRunRejectsSessionSearchAttribute(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, nil),
			},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := client.OneShotRun(
		context.Background(),
		nil,
		WithSearchAttributes(map[string]any{
			"SessionID": "sess-1",
		}),
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SessionID is not allowed for one-shot runs")
}

// TestStartRunNeverSetsEngineRunTimeout proves that no policy shape (a
// generous budget, no budget at all) ever projects onto the engine-level
// WorkflowStartRequest.RunTimeout. Active-time enforcement belongs solely to
// the workflow's Budget and Hard deadlines; a suspension carries their
// remaining durations into the continuation workflow.
func TestStartRunNeverSetsEngineRunTimeout(t *testing.T) {
	cases := []struct {
		name   string
		policy RunPolicy
	}{
		{name: "with policy budget", policy: RunPolicy{TimeBudget: 20 * time.Minute, FinalizerGrace: 10 * time.Second}},
		{name: "without policy budget", policy: RunPolicy{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eng := &stubEngine{}
			rt := &Runtime{
				Engine:  eng,
				logger:  telemetry.NoopLogger{},
				metrics: telemetry.NoopMetrics{},
				tracer:  telemetry.NoopTracer{},
				Store:   newTestStore(),
				agents: map[agent.Ident]AgentRegistration{
					"service.agent": {
						Definition: testAgentDefinition("service.agent", "service.workflow", "svc.queue", nil, nil),
						Policy:     tc.policy,
						ResumeActivityOptions: engine.ActivityOptions{
							StartToCloseTimeout: 20 * time.Second,
						},
					},
				},
			}
			client := rt.MustClient(agent.Ident("service.agent"))
			_, err := createSessionForTest(context.Background(), rt.Store, "sess-1")
			require.NoError(t, err)
			_, err = client.Start(context.Background(), "sess-1", nil, WithRunID("run-1"))
			require.NoError(t, err)
			require.Zero(t, eng.last.RunTimeout)
		})
	}
}

func TestRegisterAgentUsesDefinitionRouteAndAuthoredActivityQueues(t *testing.T) {
	eng := &stubEngine{}
	rt := New(newTestStore(), WithEngine(eng))

	err := rt.RegisterAgent(context.Background(), AgentRegistration{Definition: testRegistrationDefinition("service.agent",

		engine.WorkflowDefinition{
			Name:      "service.workflow",
			TaskQueue: "service.queue",
			Handler:   rt.ExecuteWorkflow,
		}, nil),

		WorkflowHandler: (engine.WorkflowDefinition{
			Name:      "service.workflow",
			TaskQueue: "service.queue",
			Handler:   rt.ExecuteWorkflow,
		}).Handler, Planner: &stubPlanner{},

		PlanActivityName: "service.agent.plan",
		PlanActivityOptions: engine.ActivityOptions{
			Queue: "plan.queue", StartToCloseTimeout: time.Minute,
		},
		ResumeActivityName: "service.agent.resume",
		ResumeActivityOptions: engine.ActivityOptions{
			Queue: "resume.queue", StartToCloseTimeout: time.Minute,
		},
		ExecuteToolActivity: "service.agent.executetool",
		ExecuteToolActivityOptions: engine.ActivityOptions{
			Queue: "tool.queue", StartToCloseTimeout: 2 * time.Minute,
			RetryPolicy: engine.RetryPolicy{
				MaxAttempts: 1,
			},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "service.workflow", eng.registeredWorkflow.Name)
	require.Equal(t, "service.queue", eng.registeredWorkflow.TaskQueue)
	require.NotNil(t, eng.registeredWorkflow.Handler)

	planOpts := eng.registeredPlannerActivityOptions["service.agent.plan"]
	require.Equal(t, "plan.queue", planOpts.Queue)
	require.Equal(t, time.Minute, planOpts.StartToCloseTimeout)
	require.Zero(t, planOpts.ScheduleToStartTimeout)
	require.Zero(t, planOpts.HeartbeatTimeout)

	resumeOpts := eng.registeredPlannerActivityOptions["service.agent.resume"]
	require.Equal(t, "resume.queue", resumeOpts.Queue)
	require.Equal(t, time.Minute, resumeOpts.StartToCloseTimeout)
	require.Zero(t, resumeOpts.ScheduleToStartTimeout)
	require.Zero(t, resumeOpts.HeartbeatTimeout)

	executeOpts := eng.registeredExecuteActivityOptions["service.agent.executetool"]
	require.Equal(t, "tool.queue", executeOpts.Queue)
	require.Equal(t, 2*time.Minute, executeOpts.StartToCloseTimeout)
	require.Zero(t, executeOpts.ScheduleToStartTimeout)
	require.Zero(t, executeOpts.HeartbeatTimeout)
	require.Equal(t, 1, executeOpts.RetryPolicy.MaxAttempts)

	recordOpts := eng.registeredStorageActivityOptions[storageActivityName]
	require.Equal(t, 15*time.Second, recordOpts.StartToCloseTimeout)
	require.Equal(t, 3, recordOpts.RetryPolicy.MaxAttempts)
	require.Equal(t, time.Second, recordOpts.RetryPolicy.InitialInterval)
	require.InDelta(t, 2.0, recordOpts.RetryPolicy.BackoffCoefficient, 0.000001)

	agentChildOpts := eng.registeredAgentChildOptions[agentChildActivityName]
	require.Equal(t, 2*time.Minute, agentChildOpts.StartToCloseTimeout)
	require.Equal(t, 3, agentChildOpts.RetryPolicy.MaxAttempts)
	require.Equal(t, time.Second, agentChildOpts.RetryPolicy.InitialInterval)
	require.InDelta(t, 2.0, agentChildOpts.RetryPolicy.BackoffCoefficient, 0.000001)
}

func TestRegisterAgentRejectsNegativeRecoveryTurns(t *testing.T) {
	rt := New(newTestStore())

	err := rt.RegisterAgent(context.Background(), AgentRegistration{Definition: testRegistrationDefinition("service.agent",

		engine.WorkflowDefinition{
			Name:      "service.workflow",
			TaskQueue: "service.queue",
			Handler:   rt.ExecuteWorkflow,
		}, nil),

		WorkflowHandler: (engine.WorkflowDefinition{
			Name:      "service.workflow",
			TaskQueue: "service.queue",
			Handler:   rt.ExecuteWorkflow,
		}).Handler, Planner: &stubPlanner{},

		PlanActivityName:    "service.agent.plan",
		ResumeActivityName:  "service.agent.resume",
		ExecuteToolActivity: "service.agent.executetool",
		Policy: RunPolicy{
			MaxRecoveryTurns: -1,
		},
	})

	require.ErrorIs(t, err, ErrInvalidConfig)
}

func TestAgentDefinitionRejectsTerminalSpecWithoutBookkeeping(t *testing.T) {
	require.PanicsWithValue(t,
		`runtime: invalid agent definition: invalid configuration: terminal tool "workflow.complete" must also declare bookkeeping`,
		func() {
			testAgentDefinition(
				"service.agent", "service.workflow", "service.queue",
				[]tools.ToolSpec{newInvalidTerminalSpec("workflow.complete")}, nil)
		},
	)
}

func TestRegistrationRejectsGeneratedContinuationToolName(t *testing.T) {
	reservedName := continuationActionName("svc.tools.continue_results", "source-1")
	spec := newAnyJSONSpec(reservedName)

	t.Run("agent", func(t *testing.T) {
		require.PanicsWithValue(t, fmt.Sprintf(
			`runtime: invalid agent definition: invalid configuration: tool name %q matches the runtime-generated continuation format "continue_" followed by 24 lowercase hexadecimal characters`,
			reservedName,
		), func() {
			testAgentDefinition("service.agent", "service.workflow", "service.queue", []tools.ToolSpec{spec}, nil)
		})
	})

	t.Run("toolset", func(t *testing.T) {
		rt := New(newTestStore())
		err := rt.RegisterToolset(ToolsetRegistration{
			Name: "svc.tools",
			Execute: wrapExecute(func(context.Context, *ToolCall) (*planner.ToolResult, error) {
				return &planner.ToolResult{}, nil
			}),
			Specs: []tools.ToolSpec{spec},
		})

		require.ErrorIs(t, err, ErrInvalidConfig)
		require.ErrorContains(t, err, fmt.Sprintf(
			`tool name %q matches the runtime-generated continuation format "continue_" followed by 24 lowercase hexadecimal characters`,
			reservedName,
		))
	})

	t.Run("similar authored name", func(t *testing.T) {
		rt := New(newTestStore())
		err := rt.RegisterToolset(ToolsetRegistration{
			Name: "svc.tools",
			Execute: wrapExecute(func(context.Context, *ToolCall) (*planner.ToolResult, error) {
				return &planner.ToolResult{}, nil
			}),
			Specs: []tools.ToolSpec{newAnyJSONSpec("continue_authored_tool")},
		})

		require.NoError(t, err)
	})
}

func TestRegisterToolsetRejectsEmptyToolMetadataTitle(t *testing.T) {
	rt := New(newTestStore())
	toolID := tools.Ident("svc.tools.fetch")
	err := rt.RegisterToolset(ToolsetRegistration{
		Name: "svc.tools",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{}, nil
		}),
		Specs: []tools.ToolSpec{newAnyJSONSpec(toolID)},
		ToolMetadataLookup: func(name tools.Ident) (policy.ToolMetadata, bool) {
			return policy.ToolMetadata{
				ID:          name,
				BudgetClass: policy.ToolBudgetClassBudgeted,
			}, true
		},
	})

	require.ErrorIs(t, err, ErrInvalidConfig)
	require.ErrorContains(t, err, "policy metadata title for tool \"svc.tools.fetch\" is required")
}

func TestRunOptionsPropagateToStartRequest(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:  eng,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {Definition: testAgentDefinition("service.agent", "service.workflow", "q", nil, nil)},
		},
	}

	meta := map[string]any{"source": "test"}
	memo := map[string]any{"wf": "name"}
	sa := map[string]any{"SessionID": "sess-1"}

	in := RunInput{
		AgentID:   "service.agent",
		SessionID: "sess-1",
	}
	for _, o := range []RunOption{
		WithRunID("run-1"),
		WithTurnID("turn-1"),
		WithMetadata(meta),
		WithTaskQueue("custom.q"),
		WithMemo(memo),
		WithSearchAttributes(sa),
	} {
		o(&in)
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := createSessionForTest(context.Background(), rt.Store, in.SessionID)
	require.NoError(t, err)
	_, err = client.Start(
		context.Background(),
		in.SessionID,
		nil,
		WithRunID(in.RunID),
		WithTurnID(in.TurnID),
		WithMetadata(in.Metadata),
		WithTaskQueue(in.WorkflowOptions.TaskQueue),
		WithMemo(in.WorkflowOptions.Memo),
		WithSearchAttributes(in.WorkflowOptions.SearchAttributes),
	)
	require.NoError(t, err)

	// Engine request
	require.Equal(t, "custom.q", eng.last.TaskQueue)
	require.Equal(t, "service.workflow", eng.last.Workflow)
	require.Equal(t, memo, eng.last.Memo)
	require.Equal(t, sa, eng.last.SearchAttributes)

	// Input payload
	inPtr := eng.last.Input
	require.Equal(t, "sess-1", inPtr.SessionID)
	require.Equal(t, "turn-1", inPtr.TurnID)
	require.Equal(t, meta, inPtr.Metadata)
}

func TestRecoveryFinishFinalizesWithoutConsumingTurn(t *testing.T) {
	failSpec := newAnyJSONSpec("fail")
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.tools": {Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
				return &planner.ToolResult{
					Name:    call.Name,
					Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "boom"),
				}, nil
			})},
		},
		Bus:     noopHooks{},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	seedTestToolset(rt, "svc.tools", failSpec)
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		asyncResult: ToolOutput{Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "boom")},
	}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "fail-call",
		Name:       tools.Ident("fail"),
		Payload:    rawjson.Message(`{}`),
	}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{Definition: testRegistrationDefinition(input.AgentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
		Policy:              RunPolicy{MaxRecoveryTurns: 1},
	}, input, base, initial, initialCaps(RunPolicy{MaxRecoveryTurns: 1}), time.Time{}, time.Time{}, "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool required finalization")
}

func TestStartRunForwardsWorkflowOptions(t *testing.T) {
	eng := &stubEngine{}
	rt := &Runtime{
		Engine: eng,
		Store:  newTestStore(),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {Definition: testAgentDefinition("service.agent", "service.workflow", "defaultq", nil, nil)},
		},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	in := RunInput{
		RunID:     "run-x",
		SessionID: "sess-1",
		WorkflowOptions: &WorkflowOptions{
			TaskQueue:        "customq",
			Memo:             map[string]any{"k": "v"},
			SearchAttributes: map[string]any{"sa": "x"},
		},
	}
	client := rt.MustClient(agent.Ident("service.agent"))
	_, err := createSessionForTest(context.Background(), rt.Store, in.SessionID)
	require.NoError(t, err)
	_, err = client.Start(context.Background(), in.SessionID, nil, WithRunID(in.RunID), WithWorkflowOptions(in.WorkflowOptions))
	require.NoError(t, err)
	require.Equal(t, "customq", eng.last.TaskQueue)
	require.Equal(t, in.RunID, eng.last.ID)
	require.Equal(t, in.WorkflowOptions.Memo, eng.last.Memo)
	require.Equal(t, map[string]any{
		"sa": "x",
	}, eng.last.SearchAttributes)
	require.Zero(t, eng.last.RetryPolicy)
}

func TestRegisterAgentAfterFirstRunIsRejected(t *testing.T) {
	t.Parallel()
	eng := &stubEngine{}
	rt := &Runtime{
		Engine:   eng,
		logger:   telemetry.NoopLogger{},
		metrics:  telemetry.NoopMetrics{},
		tracer:   telemetry.NoopTracer{},
		Store:    newTestStore(),
		agents:   make(map[agent.Ident]AgentRegistration),
		toolsets: make(map[string]ToolsetRegistration),
	}
	// Register initial agent so we can start a run
	err := rt.RegisterAgent(context.Background(), AgentRegistration{Definition: testRegistrationDefinition("service.agent",

		engine.WorkflowDefinition{
			Name:      "service.workflow",
			TaskQueue: "q",
			Handler: func(wfctx engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
				return &RunOutput{AgentID: "service.agent", RunID: "r1"}, nil
			},
		}, nil),

		WorkflowHandler: (engine.WorkflowDefinition{
			Name:      "service.workflow",
			TaskQueue: "q",
			Handler: func(wfctx engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
				return &RunOutput{AgentID: "service.agent", RunID: "r1"}, nil
			},
		}).Handler, Planner: &stubPlanner{},

		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	})
	require.NoError(t, err)

	// First run closes registration
	_, err = createSessionForTest(context.Background(), rt.Store, "sess-1")
	require.NoError(t, err)
	_, err = rt.MustClient(agent.Ident("service.agent")).Start(
		context.Background(), "sess-1", nil, WithRunID("run-1"),
	)
	require.NoError(t, err)
	require.Equal(t, 1, eng.sealCalls)

	// Registering a new agent afterwards is rejected
	err = rt.RegisterAgent(context.Background(), AgentRegistration{Definition: testRegistrationDefinition("service.other",

		engine.WorkflowDefinition{
			Name:      "service.other.workflow",
			TaskQueue: "q",
			Handler:   func(wfctx engine.WorkflowContext, input *RunInput) (*RunOutput, error) { return &RunOutput{}, nil },
		}, nil),

		WorkflowHandler: (engine.WorkflowDefinition{
			Name:      "service.other.workflow",
			TaskQueue: "q",
			Handler:   func(wfctx engine.WorkflowContext, input *RunInput) (*RunOutput, error) { return &RunOutput{}, nil },
		}).Handler, Planner: &stubPlanner{},

		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	})
	require.ErrorIs(t, err, ErrRegistrationClosed)
}

func TestSealClosesRegistrationAndDelegatesToEngine(t *testing.T) {
	t.Parallel()

	eng := &stubEngine{}
	rt := &Runtime{
		Engine:   eng,
		logger:   telemetry.NoopLogger{},
		metrics:  telemetry.NoopMetrics{},
		tracer:   telemetry.NoopTracer{},
		Store:    newTestStore(),
		agents:   make(map[agent.Ident]AgentRegistration),
		toolsets: make(map[string]ToolsetRegistration),
	}

	require.NoError(t, rt.Seal(context.Background()))
	require.Equal(t, 1, eng.sealCalls)

	err := rt.RegisterToolset(ToolsetRegistration{
		Name: "svc.toolset",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{}, nil
		}),
	})
	require.ErrorIs(t, err, ErrRegistrationClosed)

	require.NoError(t, rt.Seal(context.Background()))
	require.Equal(t, 1, eng.sealCalls)
}

func TestSealRetriesAfterActivationFailure(t *testing.T) {
	t.Parallel()

	eng := &stubEngine{sealErrors: []error{errors.New("temporal unavailable"), nil}}
	rt := &Runtime{
		Engine:   eng,
		logger:   telemetry.NoopLogger{},
		metrics:  telemetry.NoopMetrics{},
		tracer:   telemetry.NoopTracer{},
		Store:    newTestStore(),
		agents:   make(map[agent.Ident]AgentRegistration),
		toolsets: make(map[string]ToolsetRegistration),
	}

	err := rt.Seal(context.Background())
	require.ErrorContains(t, err, "temporal unavailable")
	require.Equal(t, 1, eng.sealCalls)

	err = rt.RegisterToolset(ToolsetRegistration{
		Name: "svc.toolset",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{}, nil
		}),
	})
	require.ErrorIs(t, err, ErrRegistrationClosed)

	require.NoError(t, rt.Seal(context.Background()))
	require.Equal(t, 2, eng.sealCalls)

	require.NoError(t, rt.Seal(context.Background()))
	require.Equal(t, 2, eng.sealCalls)
}

func TestTimeBudgetExceeded(t *testing.T) {
	toolSpec := newAnyJSONSpec("tool")
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{"svc.ts": {Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name: call.Name,
			}, nil
		})}},
		Bus:     noopHooks{},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
	}
	seedTestToolset(rt, "svc.ts", toolSpec)
	wfCtx := &testWorkflowContext{ctx: context.Background(), asyncResult: ToolOutput{Payload: []byte("null")}}
	input := &RunInput{AgentID: "svc.agent", RunID: "run-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: input.RunID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: input.AgentID, runID: input.RunID})}
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "tool-call",
		Name:       tools.Ident("tool"),
		Payload:    rawjson.Message(`{}`),
	}}}
	_, err := rt.runLoop(wfCtx, AgentRegistration{Definition: testRegistrationDefinition(input.AgentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, input, base, initial, initialCaps(RunPolicy{MaxToolCalls: 1}), wfCtx.Now().Add(-time.Second), wfCtx.Now().Add(-time.Second), "", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "time budget exceeded")
}

func TestOverridePolicy_AppliesToNewRuns_MaxToolCalls(t *testing.T) {
	agentID := agent.Ident("svc.agent")
	rt := &Runtime{
		agents: map[agent.Ident]AgentRegistration{
			agentID: {
				Definition: testAgentDefinition(agentID, "svc.agent.workflow", "test", nil, nil),
				Policy:     RunPolicy{MaxToolCalls: 5},
			},
		},
	}

	// Override policy to allow only 1 tool call.
	require.NoError(t, rt.OverridePolicy(agentID, RunPolicy{MaxToolCalls: 1}))

	reg := rt.agents[agentID]
	require.Equal(t, 1, reg.Policy.MaxToolCalls)

	// New runs should see the overridden caps when initializing caps state.
	caps := initialCaps(reg.Policy)
	require.Equal(t, 1, caps.MaxToolCalls)
	require.Equal(t, 1, caps.RemainingToolCalls)
}

func TestOverridePolicyRejectsNegativeRecoveryTurns(t *testing.T) {
	agentID := agent.Ident("svc.agent")
	rt := &Runtime{
		agents: map[agent.Ident]AgentRegistration{
			agentID: {Definition: testAgentDefinition(agentID, "svc.agent.workflow", "test", nil, nil)},
		},
	}

	err := rt.OverridePolicy(agentID, RunPolicy{MaxRecoveryTurns: -1})

	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Zero(t, rt.agents[agentID].Policy.MaxRecoveryTurns)
}

func TestConvertRunOutputToToolResult(t *testing.T) {
	t.Run("aggregates_telemetry_without_error", func(t *testing.T) {
		out := RunOutput{
			Final: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "final"}}},
			ToolEvents: []*api.ToolEvent{
				{Telemetry: &telemetry.ToolTelemetry{TokensUsed: 10, DurationMs: 100, Model: "m1"}},
				{Telemetry: &telemetry.ToolTelemetry{TokensUsed: 5, DurationMs: 50, Model: "m1"}},
			},
		}
		tr, err := ConvertRunOutputToToolResult("parent.tool", &out)
		require.NoError(t, err)
		require.Nil(t, tr.Failure)
		require.NotNil(t, tr.Telemetry)
		require.Equal(t, 15, tr.Telemetry.TokensUsed)
		require.Equal(t, int64(150), tr.Telemetry.DurationMs)
		require.Equal(t, "m1", tr.Telemetry.Model)
		require.Equal(t, "final", tr.Result)
	})
	t.Run("keeps_historical_failures_in_child_run", func(t *testing.T) {
		out := RunOutput{
			Final: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "final"}}},
			ToolEvents: []*api.ToolEvent{
				{Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "e1")},
				{Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "e2")},
			},
		}
		tr, err := ConvertRunOutputToToolResult("parent.tool", &out)
		require.NoError(t, err)
		require.Nil(t, tr.Failure)
		require.Equal(t, "final", tr.Result)
	})
}

func TestAgentAsToolNestedUpdates(t *testing.T) {
	recorder := &recordingHooks{}
	rt := &Runtime{
		Bus:            recorder,
		logger:         telemetry.NoopLogger{},
		metrics:        telemetry.NoopMetrics{},
		tracer:         telemetry.NoopTracer{},
		Store:          newTestStore(),
		agentToolSpecs: make(map[agent.Ident][]tools.ToolSpec),
	}
	_, err := createSessionForTest(context.Background(), rt.Store, "session-1")
	require.NoError(t, err)
	admitRunForTest(t, rt.Store, session.RunMeta{
		AgentID: "parent.agent", RunID: "run-parent", SessionID: "session-1",
		Status: session.RunStatusRunning,
	})

	// Register nested tools toolset used by nested agent
	rt.toolsets = map[string]ToolsetRegistration{
		"nested.tools": {
			Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
				return &planner.ToolResult{
					Name:   call.Name,
					Result: map[string]string{"ok": "true"},
				}, nil
			}),
		},
	}
	seedTestToolset(
		rt,
		"nested.tools",
		newAnyJSONSpec("child1"),
		newAnyJSONSpec("child2"),
		newAnyJSONSpec("child3"),
	)
	rt.agentToolSpecs["nested.agent"] = []tools.ToolSpec{
		rt.toolSpecs["child1"],
		rt.toolSpecs["child2"],
		rt.toolSpecs["child3"],
	}

	// Register nested agent (planner + activity names)
	nestedReg := AgentRegistration{Definition: testRegistrationDefinition("nested.agent", engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: &nestedPlannerStub{},
		PlanActivityName:    "nested.plan",
		ResumeActivityName:  "nested.resume",
		ExecuteToolActivity: "nested.execute",
		Policy:              RunPolicy{MaxToolCalls: 10},
	}
	plannerRoutes := map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
		"nested.plan": func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
			return rt.PlanStartActivity(ctx, input)
		},
		"nested.resume": func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
			return rt.PlanResumeActivity(ctx, input)
		},
		//nolint:unparam // error return is part of map type signature; test stub always succeeds
		"resume": func(_ context.Context, _ *PlanActivityInput) (*PlanActivityOutput, error) {
			return &PlanActivityOutput{
				PublicationBatchID: testPublicationBatchID,
				Result: &PlanResult{
					FinalResponse: &planner.FinalResponse{
						Message: &model.Message{
							Role: "assistant",
							Parts: []model.Part{
								model.TextPart{
									Text: "done",
								},
							},
						},
					},
				},
				Transcript: nil,
			}, nil
		},
	}
	toolRoutes := map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
		"nested.execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
			return rt.ExecuteToolActivity(ctx, input)
		},
		"execute": func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
			return rt.ExecuteToolActivity(ctx, input)
		},
	}
	wfCtx := &routeWorkflowContext{
		ctx:           context.Background(),
		runID:         "run-parent",
		plannerRoutes: plannerRoutes,
		toolRoutes:    toolRoutes,
		hookRuntime:   rt,
		childRuntime:  rt,
	}

	// Parent agent-tools toolset that invokes nested agent inline
	agentTools := ToolsetRegistration{
		Name: "svc.agenttools",
		Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call == nil {
				return nil, fmt.Errorf("tool request is nil")
			}
			wf := engine.WorkflowContextFromContext(ctx)
			if wf == nil {
				wf = wfCtx
			}
			msgs := []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "go"}}}}
			nestedCtx := run.Context{
				RunID:            NestedRunIDForToolCall(call.RunID, call.Name, call.ToolCallID),
				ParentToolCallID: call.ToolCallID,
				ParentRunID:      call.RunID,
				ParentAgentID:    call.AgentID,
				SessionID:        call.SessionID,
				TurnID:           call.TurnID,
				Tool:             call.Name,
			}
			// Inject nested agent registration into runtime for lookup
			rt.mu.Lock()
			rt.agents = map[agent.Ident]AgentRegistration{"nested.agent": nestedReg}
			rt.mu.Unlock()
			outPtr, err := rt.ExecuteAgentChild(wf, nestedReg.Definition, msgs, nestedCtx)
			if err != nil {
				return nil, err
			}
			if outPtr == nil {
				return nil, fmt.Errorf("nil nested output")
			}
			result, err := ConvertRunOutputToToolResult(call.Name, outPtr)
			if err != nil {
				return nil, err
			}
			return &result, nil
		}),
	}
	// Register parent toolset
	rt.mu.Lock()
	rt.toolsets[agentTools.Name] = agentTools
	seedTestToolset(rt, agentTools.Name, newAnyJSONSpec("invoke"))
	rt.mu.Unlock()

	// Parent run requests a single agent-tool invocation
	parentInput := &RunInput{AgentID: "parent.agent", RunID: "run-parent", SessionID: "session-1", TurnID: "turn-1"}
	base := &planner.PlanInput{RunContext: run.Context{RunID: parentInput.RunID, SessionID: parentInput.SessionID, TurnID: parentInput.TurnID}, Agent: newAgentContext(agentContextOptions{runtime: rt, agentID: parentInput.AgentID, runID: parentInput.RunID})}
	initial := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "invoke-call",
		Name:       tools.Ident("invoke"),
		Payload:    rawjson.Message(`{}`),
	}}}

	_, err = rt.runLoop(wfCtx, AgentRegistration{Definition: testRegistrationDefinition(parentInput.AgentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: &stubPlanner{},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}, parentInput, base, initial, initialCaps(RunPolicy{MaxToolCalls: 3}), time.Time{}, time.Time{}, parentInput.TurnID, nil)
	require.NoError(t, err)

	// Assert ToolCallUpdatedEvent emitted twice with counts 2 then 3 referencing parent tool call id
	var updates []*hooks.ToolCallUpdatedEvent
	for _, evt := range recorder.events {
		if u, ok := evt.(*hooks.ToolCallUpdatedEvent); ok {
			updates = append(updates, u)
		}
	}
	require.GreaterOrEqual(t, len(updates), 2)
	require.Equal(t, 2, updates[0].ExpectedChildrenTotal)
	require.Equal(t, 3, updates[1].ExpectedChildrenTotal)
}

func TestValidateAgentToolCoverage(t *testing.T) {
	ids := []tools.Ident{"a", "b"}
	// Missing both should fail.
	err := ValidateAgentToolCoverage(nil, nil, ids)
	require.Error(t, err)

	// Duplicate for A
	err = ValidateAgentToolCoverage(
		map[tools.Ident]string{"a": "x"},
		map[tools.Ident]*template.Template{"a": template.Must(template.New("t").Parse("{{.}}"))},
		ids,
	)
	require.Error(t, err)

	// OK: A text, B template
	err = ValidateAgentToolCoverage(
		map[tools.Ident]string{"a": "x"},
		map[tools.Ident]*template.Template{"b": template.Must(template.New("t").Parse("{{.}}"))},
		ids,
	)
	require.NoError(t, err)
}

func TestChildTrackerLifecycle(t *testing.T) {
	tracker := newChildTracker("parent-1")

	require.True(t, tracker.registerDiscovered([]string{"child-1", "child-2"}))
	require.Equal(t, 2, tracker.currentTotal())
	require.True(t, tracker.needsUpdate())

	tracker.markUpdated()
	require.False(t, tracker.needsUpdate())

	require.False(t, tracker.registerDiscovered([]string{"child-2"})) // duplicate ignored
	require.True(t, tracker.registerDiscovered([]string{"child-3"}))
	require.Equal(t, 3, tracker.currentTotal())
	require.True(t, tracker.needsUpdate())
}

func TestExecuteToolCallsPublishesChildUpdates(t *testing.T) {
	recorder := &recordingHooks{}
	child1Spec := newAnyJSONSpec("child1")
	child2Spec := newAnyJSONSpec("child2")
	rt := &Runtime{
		toolsets: map[string]ToolsetRegistration{
			"svc.export": {
				Execute: wrapExecute(func(ctx context.Context, call *ToolCall) (*planner.ToolResult, error) {
					return &planner.ToolResult{
						Name: call.Name,
					}, nil
				}),
			},
		},
		Bus:     recorder,
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
	}
	seedTestToolset(rt, "svc.export", child1Spec, child2Spec)
	wfCtx := &testWorkflowContext{
		ctx:         context.Background(),
		hookRuntime: rt,
		asyncResult: ToolOutput{Payload: []byte("null")},
	}
	tracker := newChildTracker("parent-123")
	calls := []ToolCall{
		{ToolCallID: "child-call-1", Name: tools.Ident("child1")},
		{ToolCallID: "child-call-2", Name: tools.Ident("child2")},
	}
	childCtx := &run.Context{
		RunID:            "run-1",
		SessionID:        "session-1",
		TurnID:           "turn-1",
		ParentRunID:      "run-parent",
		ParentAgentID:    "agent-parent",
		ParentToolCallID: "parent-123",
	}
	_, _, err := rt.executeToolCalls(wfCtx, "execute", engine.ActivityOptions{}, "agent-1", childCtx, nil, calls, 0, tracker, time.Time{})
	require.NoError(t, err)

	var update *hooks.ToolCallUpdatedEvent
	for _, evt := range recorder.events {
		if e, ok := evt.(*hooks.ToolCallUpdatedEvent); ok {
			update = e
			break
		}
	}
	require.NotNil(t, update)
	require.Equal(t, "parent-123", update.ToolCallID)
	require.Equal(t, 2, update.ExpectedChildrenTotal)
}

func TestRuntimePublishesPolicyDecision(t *testing.T) {
	bus := hooks.NewBus()
	decision := policy.Decision{
		AllowedTools: []tools.Ident{tools.Ident("search")},
		Caps: policy.CapsState{
			MaxToolCalls:           5,
			RemainingToolCalls:     5,
			MaxRecoveryTurns:       policy.DefaultMaxRecoveryTurns,
			RemainingRecoveryTurns: policy.DefaultMaxRecoveryTurns,
		},
		Labels: map[string]string{
			"policy_engine": "basic",
		},
		Metadata: map[string]any{
			"engine": "basic",
		},
	}
	rt := &Runtime{
		Policy: &stubPolicyEngine{decision: decision},
		Bus:    bus,
		policyToolMetadata: map[tools.Ident]policy.ToolMetadata{
			"search": {
				ID:          "search",
				Title:       "Search",
				BudgetClass: policy.ToolBudgetClassBudgeted,
			},
		},
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			"search": newAnyJSONSpec("search"),
		},
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Store:   newTestStore(),
		models:  make(map[string]model.Client),
	}

	var policyEvent *hooks.PolicyDecisionEvent
	sub, err := bus.Register(hooks.SubscriberFunc(func(ctx context.Context, evt hooks.Event) error {
		if e, ok := evt.(*hooks.PolicyDecisionEvent); ok {
			policyEvent = e
		}
		return nil
	}))
	require.NoError(t, err)
	defer func() {
		if err := sub.Close(); err != nil {
			t.Logf("subscriber close error: %v", err)
		}
	}()

	input := RunInput{
		AgentID:   "svc.agent",
		RunID:     "run-123",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Labels: map[string]string{
			"account": "acme",
		},
	}
	_, sessionErr := createSessionForTest(context.Background(), rt.Store, input.SessionID)
	require.NoError(t, sessionErr)
	base := &planner.PlanInput{
		Messages: []*model.Message{
			{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}},
		},
		RunContext: run.Context{
			RunID:     input.RunID,
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Labels:    cloneLabels(input.Labels),
		},
		Agent: newAgentContext(agentContextOptions{
			runtime: rt,
			agentID: input.AgentID,
			runID:   input.RunID,
		}),
	}

	wfCtx := &testWorkflowContext{
		ctx:           context.Background(),
		hookRuntime:   rt,
		asyncResult:   ToolOutput{Payload: []byte("null")},
		planResult:    &PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}

	started, err := prepareHookRecordInput(
		wfCtx.Context(),
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, input.SessionID, input.ParentRunID, "", input.Labels),
		input.TurnID,
	)
	require.NoError(t, err)
	_, err = rt.executeStorageCommand(context.Background(), &api.StorageActivityCommand{
		RootStart: &api.RootRunStartCommand{Started: started},
	})
	require.NoError(t, err)

	initial := &PlanResult{
		ToolCalls: []ToolCall{
			{
				ToolCallID: "search-call",
				Name:       tools.Ident("search"),
				Payload:    rawjson.Message([]byte(`{"query":"status"}`)),
			},
		},
	}
	caps := initialCaps(RunPolicy{MaxToolCalls: 5})

	_, err = rt.runLoop(
		wfCtx,
		AgentRegistration{Definition: testRegistrationDefinition(input.AgentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: &stubPlanner{},
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		&input,
		base,
		initial,
		caps,
		time.Time{},
		time.Time{},
		input.TurnID,
		nil,
	)
	require.NoError(t, err)

	require.NotNil(t, policyEvent)
	require.Equal(t, hooks.PolicyDecision, policyEvent.Type())
	require.Equal(t, []tools.Ident{tools.Ident("search")}, policyEvent.AllowedTools)
	require.Equal(t, decision.Metadata, policyEvent.Metadata)
	require.Equal(t, decision.Caps, policyEvent.Caps)
	require.Equal(t, decision.Labels, policyEvent.Labels)
}
