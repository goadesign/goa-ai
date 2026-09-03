package temporal

// runtime_execute_workflow_test.go verifies that a user-input request closes a
// real Temporal workflow with a continuation checkpoint instead of waiting for
// a second workflow.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentrun "goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// testTemporalAgentDefinition builds the immutable contract generated workers
// pass to runtime registration.
func testTemporalAgentDefinition(
	id agent.Ident,
	workflowName, taskQueue string,
	specs []tools.ToolSpec,
) agentruntime.AgentDefinition {
	return agentruntime.NewAgentDefinition(
		agentruntime.AgentRoute{ID: id, WorkflowName: workflowName, DefaultTaskQueue: taskQueue},
		specs,
		nil,
		nil,
		nil,
		nil,
	)
}

const runSuspensionType = "runtime.run_suspension"

func TestPlannerOutputActivityFailureIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context) error {
		calls.Add(1)
		return temporalerrors.Wrap(planner.NewOutputContractError(
			errors.New("invalid model reply"),
		))
	}, activity.RegisterOptions{Name: "invalid-planner-output"})

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
		})
		err := workflow.ExecuteActivity(ctx, "invalid-planner-output").Get(ctx, nil)
		return temporalerrors.Wrap(err)
	})

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.True(t, temporalerrors.IsOutputContract(err))
	require.EqualValues(t, 1, calls.Load())
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.True(t, appErr.NonRetryable())
}

func TestPlannerOutputChildFailureIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(func(workflow.Context) error {
		calls.Add(1)
		return temporalerrors.Wrap(planner.NewOutputContractError(
			errors.New("invalid child reply"),
		))
	}, workflow.RegisterOptions{Name: "invalid-child-output"})

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowExecutionTimeout: time.Minute,
			RetryPolicy:              &temporal.RetryPolicy{MaximumAttempts: 3},
		})
		err := workflow.ExecuteChildWorkflow(ctx, "invalid-child-output").Get(ctx, nil)
		return temporalerrors.Wrap(err)
	})

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.True(t, temporalerrors.IsOutputContract(err))
	require.EqualValues(t, 1, calls.Load())
	var appErr *temporal.ApplicationError
	require.ErrorAs(t, err, &appErr)
	require.True(t, appErr.NonRetryable())
}

func TestPlannerPublicationRetriesImmutableBatchWithoutReplanning(t *testing.T) {
	const (
		workflowName        = "publication.workflow"
		taskQueue           = "publication.queue"
		planActivityName    = "publication.plan"
		resumeActivityName  = "publication.resume"
		executeActivityName = "publication.execute"
		runID               = "run-publication"
		sessionID           = "session-publication"
	)

	agentID := agent.Ident("publication.agent")
	plannerStub := &publicationRetryPlanner{}
	store := storageinmem.New()
	runtime := agentruntime.New(store)
	_, err := store.CreateSession(context.Background(), sessionID, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(context.Background(), agentruntime.AgentRegistration{
		Definition: testTemporalAgentDefinition(agentID, workflowName, taskQueue, nil),
		Planner:    plannerStub,
		WorkflowHandler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			return runtime.ExecuteWorkflow(wfCtx, input)
		},
		PlanActivityName:    planActivityName,
		ResumeActivityName:  resumeActivityName,
		ExecuteToolActivity: executeActivityName,
	}))

	recorder := &publicationRetryRecorder{
		activityIDs: make(map[string]struct{}),
		stored:      make(map[string]*api.RecordActivityInput),
	}
	eng := &Engine{
		defaultQueue: taskQueue,
		activityOptions: map[string]engine.ActivityOptions{
			"runtime.store": {
				StartToCloseTimeout: time.Second,
				RetryPolicy:         engine.RetryPolicy{MaxAttempts: 2},
			},
		},
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(recorder.Record, activity.RegisterOptions{Name: "runtime.store"})
	env.RegisterActivityWithOptions(runtime.PlanStartActivity, activity.RegisterOptions{Name: planActivityName})
	env.RegisterActivityWithOptions(runtime.PlanResumeActivity, activity.RegisterOptions{Name: resumeActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) (*api.RunOutput, error) {
		return runtime.ExecuteWorkflow(NewWorkflowContext(eng, ctx), &agentruntime.RunInput{
			AgentID:   agentID,
			RunID:     runID,
			SessionID: sessionID,
			TurnID:    "turn-publication",
		})
	})

	require.NoError(t, env.GetWorkflowError())
	require.EqualValues(t, 1, plannerStub.calls.Load())
	activityCalls, scheduledActivities, attempts, stored := recorder.snapshot()
	require.Equal(t, 2, activityCalls)
	require.Equal(t, 1, scheduledActivities)
	require.Len(t, attempts, 4)
	require.Equal(t, attempts[0].EventKey, attempts[2].EventKey)
	require.Equal(t, attempts[1].EventKey, attempts[3].EventKey)
	require.NotEqual(t, attempts[0].EventKey, attempts[1].EventKey)
	require.Len(t, stored, 2)
}

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
		modelToolCallID     = "model-tool-call-1"
	)

	agentID := agent.Ident("service.agent")
	questionTool := tools.Ident("assistant.ask_question")
	plannerStub := &awaitQuestionsPlanner{awaitID: awaitID, toolName: questionTool, toolCallID: modelToolCallID}
	store := storageinmem.New()
	runtime := agentruntime.New(store)
	_, err := store.CreateSession(context.Background(), sessionID, time.Now().UTC())
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(context.Background(), agentruntime.AgentRegistration{
		Definition: testTemporalAgentDefinition(agentID, workflowName, taskQueue, []tools.ToolSpec{anyJSONToolSpec(questionTool)}),
		Planner:    plannerStub,
		WorkflowHandler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			return runtime.ExecuteWorkflow(wfCtx, input)
		},
		PlanActivityName:    planActivityName,
		ResumeActivityName:  resumeActivityName,
		ExecuteToolActivity: executeActivityName,
	}))

	recorder := &hookRecorder{}
	eng := &Engine{defaultQueue: taskQueue, activityOptions: make(map[string]engine.ActivityOptions)}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(recorder.Record, activity.RegisterOptions{Name: "runtime.store"})
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
	await := out.Suspension.Pending[0].Await.Questions
	require.NotNil(t, await)
	require.NotEmpty(t, await.ToolCallID)
	require.Equal(t, modelToolCallID, await.ModelToolCallID)
	require.NotEqual(t, await.ToolCallID, await.ModelToolCallID)
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
	require.Equal(t, await.ToolCallID, awaitEvent.ToolCallID)
	suspended, ok := events[len(events)-1].(*hooks.RunSuspendedEvent)
	require.True(t, ok)
	require.Equal(t, out.Suspension.ID, suspended.SuspensionID)
}

func TestExecuteWorkflowServiceActivityCancellationClosesTemporalRunCanceled(t *testing.T) {
	t.Parallel()

	const (
		workflowName        = "service.cancel.workflow"
		taskQueue           = "service.cancel.queue"
		planActivityName    = "service.cancel.plan"
		resumeActivityName  = "service.cancel.resume"
		executeActivityName = "service.cancel.execute"
		runID               = "run-service-canceled"
		sessionID           = "session-service-canceled"
	)

	agentID := agent.Ident("service.cancel_agent")
	toolName := tools.Ident("service.cancel.cancel")
	spec := anyJSONToolSpec(toolName)
	store := storageinmem.New()
	runtime := agentruntime.New(store)
	_, err := store.CreateSession(context.Background(), sessionID, time.Now().UTC())
	require.NoError(t, err)
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	startedRecord := lifecycleRecord(t, hooks.NewRunStartedEvent(runID, agentID, sessionID, "", "", nil), "run-started", startedAt)
	canceledRecord := lifecycleRecord(t, hooks.NewRunCompletedEvent(
		runID,
		agentID,
		sessionID,
		"canceled",
		agentrun.PhaseCanceled,
		nil,
		context.Canceled,
		&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
	), "run-stopped", startedAt)
	_, err = store.StartRootRun(context.Background(), storage.RootRunStart{
		Run:     session.RunStart{AgentID: string(agentID), RunID: runID, SessionID: sessionID, StartedAt: startedAt},
		Started: startedRecord, Canceled: canceledRecord,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.RegisterAgent(context.Background(), agentruntime.AgentRegistration{
		Definition: testTemporalAgentDefinition(agentID, workflowName, taskQueue, []tools.ToolSpec{spec}),
		Planner:    &cancelingServicePlanner{toolName: toolName},
		WorkflowHandler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			return runtime.ExecuteWorkflow(wfCtx, input)
		},
		PlanActivityName:    planActivityName,
		ResumeActivityName:  resumeActivityName,
		ExecuteToolActivity: executeActivityName,
		Policy:              agentruntime.RunPolicy{MaxToolCalls: 1},
	}))
	require.NoError(t, runtime.RegisterToolset(agentruntime.ToolsetRegistration{
		Name:  "service.cancel",
		Specs: []tools.ToolSpec{spec},
		Execute: func(context.Context, *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
			return nil, temporal.NewCanceledError("superseded")
		},
	}))

	recorder := &hookRecorder{}
	eng := &Engine{defaultQueue: taskQueue, activityOptions: make(map[string]engine.ActivityOptions)}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(recorder.Record, activity.RegisterOptions{Name: "runtime.store"})
	env.RegisterActivityWithOptions(runtime.PlanStartActivity, activity.RegisterOptions{Name: planActivityName})
	env.RegisterActivityWithOptions(runtime.PlanResumeActivity, activity.RegisterOptions{Name: resumeActivityName})
	env.RegisterActivityWithOptions(runtime.ExecuteToolActivity, activity.RegisterOptions{Name: executeActivityName})
	env.ExecuteWorkflow(eng.temporalWorkflowHandler(
		func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			return runtime.ExecuteWorkflow(wfCtx, input)
		},
	), &agentruntime.RunInput{
		AgentID:   agentID,
		RunID:     runID,
		SessionID: sessionID,
		TurnID:    "turn-1",
	})

	workflowErr := env.GetWorkflowError()
	require.Error(t, workflowErr)
	require.Truef(t, temporal.IsCanceledError(workflowErr), "unexpected workflow error: %T: %v", workflowErr, workflowErr)
	var completed *hooks.RunCompletedEvent
	for _, event := range recorder.Snapshot() {
		if event, ok := event.(*hooks.RunCompletedEvent); ok {
			completed = event
		}
	}
	require.NotNil(t, completed)
	require.Equal(t, "canceled", completed.Status)
	require.Nil(t, completed.Failure)
	require.NotNil(t, completed.Cancellation)
}

// lifecycleRecord encodes a typed hook event for direct integrated-store
// setup in Temporal runtime tests.
func lifecycleRecord(t *testing.T, event hooks.Event, key string, at time.Time) *runlog.Event {
	t.Helper()
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{EventKey: key, TimestampMS: at.UnixMilli()})
	require.NoError(t, err)
	return &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   input.Payload,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	}
}

type cancelingServicePlanner struct {
	toolName tools.Ident
}

func (p *cancelingServicePlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    p.toolName,
		Payload: rawjson.Message(`{}`),
	}}}, nil
}

func (p *cancelingServicePlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, errors.New("PlanResume must not run after service cancellation")
}

type (
	// awaitQuestionsPlanner suspends its first turn and records unexpected
	// resume calls.
	awaitQuestionsPlanner struct {
		mu          sync.Mutex
		resumeCalls int
		awaitID     string
		toolName    tools.Ident
		toolCallID  string
	}

	// publicationRetryPlanner emits two records in one successful planner
	// completion so the test can fail after storing a non-empty batch prefix.
	publicationRetryPlanner struct {
		calls atomic.Int32
	}

	// publicationRetryRecorder stores planner-publication records by idempotency
	// key, fails the second first-time record, and rejects changed duplicate input.
	publicationRetryRecorder struct {
		mu            sync.Mutex
		failed        bool
		activityCalls int
		activityIDs   map[string]struct{}
		attempts      []*api.RecordActivityInput
		stored        map[string]*api.RecordActivityInput
	}

	// hookRecorder decodes runtime records and tracks suspension persistence.
	hookRecorder struct {
		mu                  sync.Mutex
		events              []hooks.Event
		suspensionPersisted bool
	}
)

// PlanStart emits two deterministic planner records and one terminal response.
func (p *publicationRetryPlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	p.calls.Add(1)
	input.Events.PlannerThought(ctx, "first publication record", nil)
	input.Events.PlannerThought(ctx, "second publication record", nil)
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "done"}},
	}}}, nil
}

// PlanResume is not part of this terminal planner flow.
func (*publicationRetryPlanner) PlanResume(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
	return nil, errors.New("publication retry planner cannot resume")
}

// Record persists non-publication records normally and exercises immutable
// idempotency for the planner completion batch.
func (r *publicationRetryRecorder) Record(ctx context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
	records := storageCommandRecords(command)
	hasPlannerPublication := false
	for _, input := range records {
		if strings.Contains(input.EventKey, "/planner-publication/") {
			hasPlannerPublication = true
			break
		}
	}
	if hasPlannerPublication {
		r.mu.Lock()
		r.activityCalls++
		r.activityIDs[activity.GetInfo(ctx).ActivityID] = struct{}{}
		r.mu.Unlock()
	}
	for _, input := range records {
		if err := r.record(input); err != nil {
			return nil, err
		}
	}
	return storageCommandResult(command, len(records)), nil
}

// record stores one item from the ordered activity input and fails once after
// the first planner record has been saved.
func (r *publicationRetryRecorder) record(input *api.RecordActivityInput) error {
	if !strings.Contains(input.EventKey, "/planner-publication/") {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cloned := cloneRecordActivityInput(input)
	r.attempts = append(r.attempts, cloned)
	if existing, ok := r.stored[input.EventKey]; ok {
		if err := equalRecordActivityInput(existing, input); err != nil {
			return err
		}
		return nil
	}
	if !r.failed && len(r.stored) == 1 {
		r.failed = true
		return errors.New("record backend unavailable")
	}
	r.stored[input.EventKey] = cloned
	return nil
}

// snapshot returns isolated publication attempts and stored records after the
// Temporal workflow has completed.
func (r *publicationRetryRecorder) snapshot() (
	int,
	int,
	[]*api.RecordActivityInput,
	map[string]*api.RecordActivityInput,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	attempts := make([]*api.RecordActivityInput, 0, len(r.attempts))
	for _, input := range r.attempts {
		attempts = append(attempts, cloneRecordActivityInput(input))
	}
	stored := make(map[string]*api.RecordActivityInput, len(r.stored))
	for key, input := range r.stored {
		stored[key] = cloneRecordActivityInput(input)
	}
	return r.activityCalls, len(r.activityIDs), attempts, stored
}

// cloneRecordActivityInput copies the complete record passed across the
// Temporal activity boundary, including its payload bytes.
func cloneRecordActivityInput(input *api.RecordActivityInput) *api.RecordActivityInput {
	cloned := *input
	cloned.Payload = append([]byte(nil), input.Payload...)
	return &cloned
}

// equalRecordActivityInput verifies that retrying one event key cannot change
// any frozen record field.
func equalRecordActivityInput(want, got *api.RecordActivityInput) error {
	if want.Type != got.Type ||
		want.EventKey != got.EventKey ||
		want.RunID != got.RunID ||
		want.AgentID != got.AgentID ||
		want.SessionID != got.SessionID ||
		want.TurnID != got.TurnID ||
		want.TimestampMS != got.TimestampMS ||
		!bytes.Equal(want.Payload, got.Payload) {
		return fmt.Errorf("duplicate planner publication record %q changed", got.EventKey)
	}
	return nil
}

func (p *awaitQuestionsPlanner) PlanStart(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
	title := "Questions"
	return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitQuestionsItem(&planner.AwaitQuestions{
		ID: p.awaitID, ToolName: p.toolName, ModelToolCallID: p.toolCallID,
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

func (r *hookRecorder) Record(_ context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
	records := storageCommandRecords(command)
	for _, input := range records {
		if err := r.record(input); err != nil {
			return nil, err
		}
	}
	recordCount := len(records)
	if command.Suspension != nil {
		recordCount = 1
	}
	return storageCommandResult(command, recordCount), nil
}

func storageCommandRecords(command *api.StorageActivityCommand) []*api.RecordActivityInput {
	switch {
	case command.Append != nil:
		return command.Append.Records
	case command.RootStart != nil:
		return []*api.RecordActivityInput{command.RootStart.Started}
	case command.ChildStart != nil:
		return []*api.RecordActivityInput{command.ChildStart.ParentLinked, command.ChildStart.Started}
	case command.OneShotStart != nil:
		return []*api.RecordActivityInput{command.OneShotStart.Started}
	case command.Cancellation != nil:
		return []*api.RecordActivityInput{command.Cancellation.Record}
	case command.Suspension != nil:
		return []*api.RecordActivityInput{command.Suspension.Checkpoint, command.Suspension.Suspended}
	default:
		return []*api.RecordActivityInput{command.Terminal.Record}
	}
}

func storageCommandResult(command *api.StorageActivityCommand, recordCount int) *api.StorageActivityResult {
	results := make([]storage.AppendResult, recordCount)
	for index := range results {
		results[index] = storage.AppendResult{ID: fmt.Sprint(index + 1), Inserted: true, SessionStatus: session.StatusActive}
	}
	switch {
	case command.Append != nil:
		return &api.StorageActivityResult{Append: &api.AppendRecordsResult{Records: results}}
	case command.RootStart != nil:
		return &api.StorageActivityResult{RootStart: &api.StartRunResult{Outcome: session.RunStartProceed, Records: results}}
	case command.ChildStart != nil:
		return &api.StorageActivityResult{ChildStart: &api.StartRunResult{Outcome: session.RunStartProceed, Records: results}}
	case command.OneShotStart != nil:
		return &api.StorageActivityResult{OneShotStart: &api.StartRunResult{Outcome: session.RunStartProceed, Records: results}}
	case command.Cancellation != nil:
		return &api.StorageActivityResult{Cancellation: &api.RunCancellationResult{Outcome: api.RunCancellationAccepted, Record: results[0]}}
	case command.Suspension != nil:
		return &api.StorageActivityResult{Suspension: &api.RecordWriteResult{Record: results[0]}}
	default:
		return &api.StorageActivityResult{Terminal: &api.RecordWriteResult{Record: results[0]}}
	}
}

// record captures one ordered item from the runtime storage activity.
func (r *hookRecorder) record(input *api.RecordActivityInput) error {
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

func anyJSONToolSpec(name tools.Ident) tools.ToolSpec {
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
		Name: name,
		Payload: tools.TypeSpec{
			Name:   string(name) + "_payload",
			Schema: rawjson.Message(`{"type":"object"}`),
			Codec:  codec,
		},
		Result: tools.TypeSpec{Name: string(name) + "_result", Codec: codec},
	}
}
