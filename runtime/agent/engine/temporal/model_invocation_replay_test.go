// These tests replay concise synthetic Temporal histories through the same
// workflow function that a worker registers. The histories contain production
// activity names and runtime API payloads; no planner activity executes during
// replay.
package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/temporalproto"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

const (
	productionReplayWorkflowName = "replay.agent.workflow"
	productionReplayTaskQueue    = "replay.agent.queue"
	productionReplayPlanActivity = "replay.agent.plan"
	productionReplayResume       = "replay.agent.resume"
	productionReplayExecute      = "replay.agent.execute"
	productionReplayRecord       = "runtime.store"
	productionReplayRunID        = "replay-run"
	productionReplaySessionID    = "replay-session"
	productionReplayTurnID       = "replay-turn"
)

var productionReplayAgentID = agent.Ident("replay.agent")

type replayPlanner struct {
	calls atomic.Int32
}

// PlanStart records an unexpected direct planner call. Temporal replay must
// consume the recorded activity result instead of invoking this method.
func (p *replayPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	p.calls.Add(1)
	return nil, errors.New("planner must not execute while replaying history")
}

// PlanResume records an unexpected direct planner call. The production workflow
// must emit a resume activity command and consume its recorded result.
func (p *replayPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	p.calls.Add(1)
	return nil, errors.New("planner must not execute while replaying history")
}

func TestProductionWorkflowReplaysPreRecoveryHistory(t *testing.T) {
	plannerStub, handler := productionReplayWorkflow(t)
	history := deserializeReplayHistory(t, syntheticProductionReplayHistory(t, &api.PlanActivityOutput{
		PublicationBatchID: "00000000-0000-4000-8000-000000000001",
		Result: &api.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "completed"}},
				},
			},
		},
	}, false))

	require.Equal(t, []string{
		productionReplayRecord,
		productionReplayRecord,
		productionReplayRecord,
		productionReplayPlanActivity,
		productionReplayRecord,
		productionReplayRecord,
		productionReplayRecord,
	}, scheduledActivityNames(history))

	replayProductionWorkflow(t, handler, history)
	assert.Zero(t, plannerStub.calls.Load())
}

func TestProductionWorkflowReplaysModelInvocationRecovery(t *testing.T) {
	plannerStub, handler := productionReplayWorkflow(t)
	correction := `Field "query" must contain a JSON string.`
	history := deserializeReplayHistory(t, syntheticProductionReplayHistory(t, &api.PlanActivityOutput{
		PublicationBatchID: "00000000-0000-4000-8000-000000000001",
		ModelInvocationRecovery: &api.ModelInvocationRecovery{
			Correction: correction,
		},
	}, true))

	resumeScheduled := history.Events[34].
		GetActivityTaskScheduledEventAttributes()
	require.Equal(t, productionReplayResume, resumeScheduled.ActivityType.Name)
	var resumeInput api.PlanActivityInput
	require.NoError(t, NewAgentDataConverter().FromPayloads(resumeScheduled.Input, &resumeInput))
	require.NotNil(t, resumeInput.ModelInvocationRecovery)
	require.Equal(t, correction, resumeInput.ModelInvocationRecovery.Correction)
	require.Equal(t, []string{
		productionReplayRecord,
		productionReplayRecord,
		productionReplayRecord,
		productionReplayPlanActivity,
		productionReplayRecord,
		productionReplayResume,
		productionReplayRecord,
		productionReplayRecord,
	}, scheduledActivityNames(history))

	replayProductionWorkflow(t, handler, history)

	assert.Zero(t, plannerStub.calls.Load())
}

func TestProductionWorkflowReplaysUnadvertisedToolNameRecovery(t *testing.T) {
	plannerStub, handler := productionReplayWorkflow(t)
	name := "$FUNCTIONS.catalog_list_nearby"
	usage := model.TokenUsage{
		Model:        "test-model",
		InputTokens:  4,
		OutputTokens: 3,
		TotalTokens:  7,
	}
	usagePayload, err := hooks.EncodeRecordPayload(hooks.NewUsageEvent(
		productionReplayRunID,
		productionReplayAgentID,
		productionReplaySessionID,
		usage,
	))
	require.NoError(t, err)
	history := deserializeReplayHistory(t, syntheticProductionReplayHistory(t, &api.PlanActivityOutput{
		PublicationBatchID: "00000000-0000-4000-8000-000000000001",
		Usage:              usage,
		PlannerEvents: []*api.PlannerEventRecord{{
			Type:    hooks.Usage,
			Payload: usagePayload,
		}},
		ModelInvocationRecovery: &api.ModelInvocationRecovery{
			UnadvertisedToolName: name,
		},
	}, true))

	resumeScheduled := scheduledActivity(t, history, productionReplayResume).
		GetActivityTaskScheduledEventAttributes()
	require.Equal(t, productionReplayResume, resumeScheduled.ActivityType.Name)
	var resumeInput api.PlanActivityInput
	require.NoError(t, NewAgentDataConverter().FromPayloads(resumeScheduled.Input, &resumeInput))
	require.NotNil(t, resumeInput.ModelInvocationRecovery)
	assert.Equal(t, name, resumeInput.ModelInvocationRecovery.UnadvertisedToolName)
	assert.Empty(t, resumeInput.ModelInvocationRecovery.Correction)
	assert.Equal(t, []string{
		productionReplayRecord,
		productionReplayRecord,
		productionReplayRecord,
		productionReplayPlanActivity,
		productionReplayRecord,
		productionReplayRecord,
		productionReplayResume,
		productionReplayRecord,
		productionReplayRecord,
	}, scheduledActivityNames(history))
	recordedOutput := recordedPlanActivityOutput(t, history, productionReplayPlanActivity)
	assert.Equal(t, usage, recordedOutput.Usage)
	require.Len(t, recordedOutput.PlannerEvents, 1)
	assert.Equal(t, hooks.Usage, recordedOutput.PlannerEvents[0].Type)
	assert.Equal(t, []model.TokenUsage{usage}, recordedUsageEvents(t, history))

	replayedOutput := replayProductionWorkflow(t, handler, history)

	assert.Zero(t, plannerStub.calls.Load())
	require.NotNil(t, replayedOutput)
	require.NotNil(t, replayedOutput.Usage)
	assert.Equal(t, model.TokenUsage{
		InputTokens:  4,
		OutputTokens: 3,
		TotalTokens:  7,
	}, *replayedOutput.Usage)
}

// productionReplayWorkflow registers the same runtime handler and activity
// names that generated worker code registers, then returns the Temporal adapter
// wrapper used by Engine.RegisterWorkflow.
func productionReplayWorkflow(
	t *testing.T,
) (*replayPlanner, func(workflow.Context, *api.RunInput) (*api.RunOutput, error)) {
	t.Helper()
	plannerStub := &replayPlanner{}
	store := storageinmem.New()
	rt := agentruntime.New(
		store,
		agentruntime.WithEngine(engineinmem.New()),
		agentruntime.WithLogger(telemetry.NoopLogger{}),
	)
	_, err := store.CreateSession(t.Context(), productionReplaySessionID, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, rt.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
		Definition:          testTemporalAgentDefinition(productionReplayAgentID, productionReplayWorkflowName, productionReplayTaskQueue, nil),
		Planner:             plannerStub,
		WorkflowHandler:     rt.ExecuteWorkflow,
		PlanActivityName:    productionReplayPlanActivity,
		ResumeActivityName:  productionReplayResume,
		ExecuteToolActivity: productionReplayExecute,
		Policy: agentruntime.RunPolicy{
			MaxRecoveryTurns: 1,
		},
	}))
	eng := &Engine{
		defaultQueue: productionReplayTaskQueue,
		activityOptions: map[string]engine.ActivityOptions{
			productionReplayRecord: {
				StartToCloseTimeout: time.Minute,
			},
			productionReplayPlanActivity: {
				StartToCloseTimeout: time.Minute,
			},
			productionReplayResume: {
				StartToCloseTimeout: time.Minute,
			},
			productionReplayExecute: {
				StartToCloseTimeout: time.Minute,
			},
		},
	}
	return plannerStub, eng.temporalWorkflowHandler(rt.ExecuteWorkflow)
}

// replayProductionWorkflow registers the production Temporal wrapper with the
// runtime data converter. Temporal compares commands emitted by Runtime.ExecuteWorkflow
// with the activity commands stored in history.
func replayProductionWorkflow(
	t *testing.T,
	handler func(workflow.Context, *api.RunInput) (*api.RunOutput, error),
	history *historypb.History,
) *api.RunOutput {
	t.Helper()
	var replayed atomic.Pointer[api.RunOutput]
	capturingHandler := func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
		output, err := handler(ctx, input)
		replayed.Store(output)
		return output, err
	}
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{
		DataConverter: NewAgentDataConverter(),
	})
	require.NoError(t, err)
	replayer.RegisterWorkflowWithOptions(
		capturingHandler,
		workflow.RegisterOptions{Name: productionReplayWorkflowName},
	)
	require.NoError(t, replayer.ReplayWorkflowHistory(nil, history))
	return replayed.Load()
}

// syntheticProductionReplayHistory records the complete command sequence from
// one production workflow run. The old form contains the PlanActivityOutput
// shape used before model-invocation recovery existed. The recovery form records
// one replacement planner turn. These histories are synthetic fixtures, not
// histories captured from a deployed Temporal service.
func syntheticProductionReplayHistory(
	t *testing.T,
	first *api.PlanActivityOutput,
	recovery bool,
) *historypb.History {
	t.Helper()
	dataConverter := NewAgentDataConverter()
	runInput := &api.RunInput{
		AgentID:   productionReplayAgentID,
		RunID:     productionReplayRunID,
		SessionID: productionReplaySessionID,
		TurnID:    productionReplayTurnID,
	}
	startInput, err := dataConverter.ToPayloads(runInput)
	require.NoError(t, err)
	firstResult, err := dataConverter.ToPayloads(first)
	require.NoError(t, err)
	rootStartResult, err := dataConverter.ToPayloads(&api.StorageActivityResult{
		RootStart: &api.StartRunResult{Outcome: session.RunStartProceed},
	})
	require.NoError(t, err)
	appendResult, err := dataConverter.ToPayloads(&api.StorageActivityResult{
		Append: &api.AppendRecordsResult{},
	})
	require.NoError(t, err)
	terminalResult, err := dataConverter.ToPayloads(&api.StorageActivityResult{
		Terminal: &api.RecordWriteResult{},
	})
	require.NoError(t, err)

	events := []*historypb.HistoryEvent{
		workflowExecutionStartedEvent(1, productionReplayWorkflowName, productionReplayTaskQueue, startInput),
		workflowTaskScheduledEvent(2),
		workflowTaskStartedEvent(3),
		workflowTaskCompletedEvent(4, 2, 3),
		activityTaskScheduledEvent(5, productionReplayRecord, nil),
		activityTaskStartedEvent(6, 5),
		activityTaskCompletedEvent(7, 5, 6, rootStartResult),
		workflowTaskScheduledEvent(8),
		workflowTaskStartedEvent(9),
		workflowTaskCompletedEvent(10, 8, 9),
		activityTaskScheduledEvent(11, productionReplayRecord, nil),
		activityTaskStartedEvent(12, 11),
		activityTaskCompletedEvent(13, 11, 12, appendResult),
		workflowTaskScheduledEvent(14),
		workflowTaskStartedEvent(15),
		workflowTaskCompletedEvent(16, 14, 15),
		activityTaskScheduledEvent(17, productionReplayRecord, nil),
		activityTaskStartedEvent(18, 17),
		activityTaskCompletedEvent(19, 17, 18, appendResult),
		workflowTaskScheduledEvent(20),
		workflowTaskStartedEvent(21),
		workflowTaskCompletedEvent(22, 20, 21),
		activityTaskScheduledEvent(23, productionReplayPlanActivity, nil),
		activityTaskStartedEvent(24, 23),
		activityTaskCompletedEvent(25, 23, 24, firstResult),
		workflowTaskScheduledEvent(26),
		workflowTaskStartedEvent(27),
		workflowTaskCompletedEvent(28, 26, 27),
	}
	if recovery {
		nextID := int64(29)
		if len(first.PlannerEvents) > 0 {
			publicationInput := plannerPublicationInput(t, first.PlannerEvents)
			events = append(events,
				activityTaskScheduledEvent(nextID, productionReplayRecord, publicationInput),
				activityTaskStartedEvent(nextID+1, nextID),
				activityTaskCompletedEvent(nextID+2, nextID, nextID+1, appendResult),
				workflowTaskScheduledEvent(nextID+3),
				workflowTaskStartedEvent(nextID+4),
				workflowTaskCompletedEvent(nextID+5, nextID+3, nextID+4),
			)
			nextID += 6
		}
		events = append(events,
			activityTaskScheduledEvent(nextID, productionReplayRecord, nil),
			activityTaskStartedEvent(nextID+1, nextID),
			activityTaskCompletedEvent(nextID+2, nextID, nextID+1, appendResult),
			workflowTaskScheduledEvent(nextID+3),
			workflowTaskStartedEvent(nextID+4),
			workflowTaskCompletedEvent(nextID+5, nextID+3, nextID+4),
		)
		resumeInput, err := dataConverter.ToPayloads(&api.PlanActivityInput{
			AgentID: productionReplayAgentID,
			RunID:   productionReplayRunID,
			RunContext: run.Context{
				RunID:     productionReplayRunID,
				SessionID: productionReplaySessionID,
				TurnID:    productionReplayTurnID,
				Attempt:   2,
			},
			ModelInvocationRecovery: first.ModelInvocationRecovery,
		})
		require.NoError(t, err)
		resumeResult, err := dataConverter.ToPayloads(&api.PlanActivityOutput{
			PublicationBatchID: "00000000-0000-4000-8000-000000000002",
			Result: &api.PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "corrected"}},
					},
				},
			},
		})
		require.NoError(t, err)
		resumeID := nextID + 6
		events = append(events,
			activityTaskScheduledEvent(resumeID, productionReplayResume, resumeInput),
			activityTaskStartedEvent(resumeID+1, resumeID),
			activityTaskCompletedEvent(resumeID+2, resumeID, resumeID+1, resumeResult),
			workflowTaskScheduledEvent(resumeID+3),
			workflowTaskStartedEvent(resumeID+4),
			workflowTaskCompletedEvent(resumeID+5, resumeID+3, resumeID+4),
			activityTaskScheduledEvent(resumeID+6, productionReplayRecord, nil),
			activityTaskStartedEvent(resumeID+7, resumeID+6),
			activityTaskCompletedEvent(resumeID+8, resumeID+6, resumeID+7, appendResult),
			workflowTaskScheduledEvent(resumeID+9),
			workflowTaskStartedEvent(resumeID+10),
			workflowTaskCompletedEvent(resumeID+11, resumeID+9, resumeID+10),
			activityTaskScheduledEvent(resumeID+12, productionReplayRecord, nil),
			activityTaskStartedEvent(resumeID+13, resumeID+12),
			activityTaskCompletedEvent(resumeID+14, resumeID+12, resumeID+13, terminalResult),
			workflowTaskScheduledEvent(resumeID+15),
			workflowTaskStartedEvent(resumeID+16),
			workflowTaskCompletedEvent(resumeID+17, resumeID+15, resumeID+16),
			workflowExecutionCompletedEvent(resumeID+18, resumeID+17),
		)
		return &historypb.History{Events: events}
	}
	events = append(events,
		activityTaskScheduledEvent(29, productionReplayRecord, nil),
		activityTaskStartedEvent(30, 29),
		activityTaskCompletedEvent(31, 29, 30, appendResult),
		workflowTaskScheduledEvent(32),
		workflowTaskStartedEvent(33),
		workflowTaskCompletedEvent(34, 32, 33),
		activityTaskScheduledEvent(35, productionReplayRecord, nil),
		activityTaskStartedEvent(36, 35),
		activityTaskCompletedEvent(37, 35, 36, appendResult),
		workflowTaskScheduledEvent(38),
		workflowTaskStartedEvent(39),
		workflowTaskCompletedEvent(40, 38, 39),
		activityTaskScheduledEvent(41, productionReplayRecord, nil),
		activityTaskStartedEvent(42, 41),
		activityTaskCompletedEvent(43, 41, 42, terminalResult),
		workflowTaskScheduledEvent(44),
		workflowTaskStartedEvent(45),
		workflowTaskCompletedEvent(46, 44, 45),
		workflowExecutionCompletedEvent(47, 46),
	)
	return &historypb.History{Events: events}
}

// scheduledActivityNames returns every activity command stored in replay order.
// The recovery test uses this list to prove that the rejected output schedules
// PlanResume and never schedules tool execution.
func scheduledActivityNames(history *historypb.History) []string {
	names := make([]string, 0)
	for _, event := range history.Events {
		attrs := event.GetActivityTaskScheduledEventAttributes()
		if attrs != nil {
			names = append(names, attrs.ActivityType.Name)
		}
	}
	return names
}

// scheduledActivity returns the one stored scheduling event for activityName.
// The replay tests use it to inspect the exact payload consumed by the workflow.
func scheduledActivity(
	t *testing.T,
	history *historypb.History,
	activityName string,
) *historypb.HistoryEvent {
	t.Helper()
	var found *historypb.HistoryEvent
	for _, event := range history.Events {
		attrs := event.GetActivityTaskScheduledEventAttributes()
		if attrs == nil || attrs.ActivityType.Name != activityName {
			continue
		}
		require.Nil(t, found, "activity %q must be scheduled once", activityName)
		found = event
	}
	require.NotNil(t, found, "activity %q was not scheduled", activityName)
	return found
}

// recordedPlanActivityOutput decodes the activity result stored in Temporal
// history so the test can compare replayed state with the exact recorded usage.
func recordedPlanActivityOutput(
	t *testing.T,
	history *historypb.History,
	activityName string,
) api.PlanActivityOutput {
	t.Helper()
	scheduledID := scheduledActivity(t, history, activityName).EventId
	for _, event := range history.Events {
		attrs := event.GetActivityTaskCompletedEventAttributes()
		if attrs == nil || attrs.ScheduledEventId != scheduledID {
			continue
		}
		var output api.PlanActivityOutput
		require.NoError(t, NewAgentDataConverter().FromPayloads(attrs.Result, &output))
		return output
	}
	require.FailNow(t, "activity result was not recorded", activityName)
	return api.PlanActivityOutput{}
}

// recordedUsageEvents decodes every usage record stored in replay history.
// The caller receives one entry per durable usage event in scheduling order.
func recordedUsageEvents(t *testing.T, history *historypb.History) []model.TokenUsage {
	t.Helper()
	var usage []model.TokenUsage
	for _, event := range history.Events {
		attrs := event.GetActivityTaskScheduledEventAttributes()
		if attrs == nil || attrs.ActivityType.Name != productionReplayRecord || attrs.Input == nil {
			continue
		}
		var command api.StorageActivityCommand
		require.NoError(t, NewAgentDataConverter().FromPayloads(attrs.Input, &command))
		if command.Append == nil {
			continue
		}
		for _, record := range command.Append.Records {
			if record.Type != hooks.Usage {
				continue
			}
			var usageEvent hooks.UsageEvent
			require.NoError(t, json.Unmarshal(record.Payload, &usageEvent))
			usage = append(usage, usageEvent.TokenUsage)
		}
	}
	return usage
}

// plannerPublicationInput serializes planner events into the same Temporal
// append-command shape used by the runtime's durable storage activity.
func plannerPublicationInput(
	t *testing.T,
	events []*api.PlannerEventRecord,
) *commonpb.Payloads {
	t.Helper()
	records := make([]*api.RecordActivityInput, 0, len(events))
	for index, event := range events {
		records = append(records, &api.RecordActivityInput{
			Type: event.Type,
			EventKey: productionReplayRunID + "/planner-publication/00000000-0000-4000-8000-000000000001/" +
				strconv.Itoa(index) + "/" + string(event.Type),
			RunID:     productionReplayRunID,
			AgentID:   productionReplayAgentID,
			SessionID: productionReplaySessionID,
			TurnID:    productionReplayTurnID,
			Payload:   event.Payload,
		})
	}
	payloads, err := NewAgentDataConverter().ToPayloads(&api.StorageActivityCommand{
		Append: &api.AppendRecordsCommand{Records: records},
	})
	require.NoError(t, err)
	return payloads
}

// deserializeReplayHistory passes each synthetic history through Temporal's
// public JSON format before replay so the test covers stored-history decoding.
func deserializeReplayHistory(t *testing.T, history *historypb.History) *historypb.History {
	t.Helper()
	data, err := (temporalproto.CustomJSONMarshalOptions{}).Marshal(history)
	require.NoError(t, err)
	decoded, err := client.HistoryFromJSON(bytes.NewReader(data), client.HistoryJSONOptions{})
	require.NoError(t, err)
	return decoded
}

func workflowExecutionStartedEvent(
	id int64,
	workflowName, taskQueue string,
	input *commonpb.Payloads,
) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{
			WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{
				WorkflowType: &commonpb.WorkflowType{Name: workflowName},
				TaskQueue:    &taskqueuepb.TaskQueue{Name: taskQueue},
				Input:        input,
			},
		},
	}
}

func workflowTaskScheduledEvent(id int64) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{
			WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{},
		},
	}
}

func workflowTaskStartedEvent(id int64) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED,
	}
}

func workflowTaskCompletedEvent(id, scheduledID, startedID int64) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED,
		Attributes: &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{
			WorkflowTaskCompletedEventAttributes: &historypb.WorkflowTaskCompletedEventAttributes{
				ScheduledEventId: scheduledID,
				StartedEventId:   startedID,
			},
		},
	}
}

func activityTaskScheduledEvent(
	id int64,
	name string,
	input *commonpb.Payloads,
) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED,
		Attributes: &historypb.HistoryEvent_ActivityTaskScheduledEventAttributes{
			ActivityTaskScheduledEventAttributes: &historypb.ActivityTaskScheduledEventAttributes{
				ActivityId:   strconv.FormatInt(id, 10),
				ActivityType: &commonpb.ActivityType{Name: name},
				TaskQueue:    &taskqueuepb.TaskQueue{Name: productionReplayTaskQueue},
				Input:        input,
			},
		},
	}
}

func activityTaskStartedEvent(id, scheduledID int64) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_STARTED,
		Attributes: &historypb.HistoryEvent_ActivityTaskStartedEventAttributes{
			ActivityTaskStartedEventAttributes: &historypb.ActivityTaskStartedEventAttributes{
				ScheduledEventId: scheduledID,
			},
		},
	}
}

func activityTaskCompletedEvent(
	id, scheduledID, startedID int64,
	result *commonpb.Payloads,
) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_COMPLETED,
		Attributes: &historypb.HistoryEvent_ActivityTaskCompletedEventAttributes{
			ActivityTaskCompletedEventAttributes: &historypb.ActivityTaskCompletedEventAttributes{
				ScheduledEventId: scheduledID,
				StartedEventId:   startedID,
				Result:           result,
			},
		},
	}
}

func workflowExecutionCompletedEvent(id, completedTaskID int64) *historypb.HistoryEvent {
	return &historypb.HistoryEvent{
		EventId:   id,
		EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED,
		Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{
			WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{
				WorkflowTaskCompletedEventId: completedTaskID,
			},
		},
	}
}
