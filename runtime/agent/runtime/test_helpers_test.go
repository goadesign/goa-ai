//nolint:lll // allow long lines in test literals for readability
package runtime

// This file provides planner, workflow, tool, and event-log fixtures shared by
// focused runtime contract tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentrun "goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type testStore struct {
	*storageinmem.Store
}

// newTestStore returns one integrated store for runtime tests.
func newTestStore() *testStore {
	return &testStore{Store: storageinmem.New()}
}

// AppendRunRecord creates the run fixture needed by tests that call an
// activity below the workflow-start boundary. Production stores reject the
// same missing-run write, and their focused tests use storageinmem.Store
// directly.
func (s *testStore) AppendRunRecord(ctx context.Context, record *runlog.Event) (storage.AppendResult, error) {
	result, err := s.Store.AppendRunRecord(ctx, record)
	if !errors.Is(err, session.ErrRunNotFound) {
		return result, err
	}
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	start := session.RunStart{
		AgentID: string(record.AgentID), RunID: record.RunID,
		SessionID: record.SessionID, StartedAt: startedAt,
	}
	started, encodeErr := encodeTestHookRecord(
		hooks.NewRunStartedEvent(record.RunID, record.AgentID, record.SessionID, "", "", nil),
		"test/start",
		startedAt,
	)
	if encodeErr != nil {
		return storage.AppendResult{}, encodeErr
	}
	if record.SessionID == "" {
		_, startErr := s.StartOneShotRun(ctx, storage.OneShotRunStart{Run: start, Started: started})
		if startErr != nil {
			return storage.AppendResult{}, startErr
		}
	} else {
		_, createErr := s.CreateSession(ctx, record.SessionID, startedAt)
		if createErr != nil && !errors.Is(createErr, session.ErrSessionEnded) {
			return storage.AppendResult{}, createErr
		}
		canceled, canceledErr := encodeTestHookRecord(hooks.NewRunCompletedEvent(
			record.RunID,
			record.AgentID,
			record.SessionID,
			"canceled",
			agentrun.PhaseCanceled,
			nil,
			context.Canceled,
			&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
		), "test/stopped", startedAt)
		if canceledErr != nil {
			return storage.AppendResult{}, canceledErr
		}
		_, startErr := s.StartRootRun(ctx, storage.RootRunStart{Run: start, Started: started, Canceled: canceled})
		if startErr != nil {
			return storage.AppendResult{}, startErr
		}
	}
	return s.Store.AppendRunRecord(ctx, record)
}

func createSessionForTest(ctx context.Context, store storage.Store, sessionID string) (session.Session, error) {
	host := store.(interface {
		CreateSession(context.Context, string, time.Time) (session.Session, error)
	})
	return host.CreateSession(ctx, sessionID, time.Now().UTC())
}

func storeSuspensionForTest(ctx context.Context, store storage.Store, runID string, value session.RunSuspension) error {
	meta, err := store.LoadRun(ctx, runID)
	if err != nil {
		return err
	}
	record, err := encodeTestHookRecord(hooks.NewRunSuspendedEvent(
		runID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		value.ID,
		"v6",
		1,
		nil,
	), terminalRunEventKey, time.Now().UTC().Truncate(time.Millisecond))
	if err != nil {
		return err
	}
	_, err = store.RecordRunSuspension(ctx, storage.RunSuspension{
		RunID:      runID,
		Suspension: value,
		Record:     record,
	})
	return err
}

// admitRunForTest creates a run through the same lifecycle operations used by
// production code, then advances it to the status required by the test.
func admitRunForTest(t testing.TB, store storage.Store, run session.RunMeta) {
	admitRunWithPredecessorForTest(t, store, run, "")
}

// admitContinuedRunForTest creates a run whose start record names the run that
// supplied its restored planner state.
func admitContinuedRunForTest(
	t testing.TB,
	store storage.Store,
	run session.RunMeta,
	predecessorRunID string,
) {
	t.Helper()
	admitRunWithPredecessorForTest(t, store, run, predecessorRunID)
}

// admitRunWithPredecessorForTest stores one run through the production
// lifecycle operation and advances it to the status requested by the test.
func admitRunWithPredecessorForTest(
	t testing.TB,
	store storage.Store,
	run session.RunMeta,
	predecessorRunID string,
) {
	t.Helper()
	if run.SessionID != "" {
		host := store.(interface {
			CreateSession(context.Context, string, time.Time) (session.Session, error)
		})
		_, err := host.CreateSession(context.Background(), run.SessionID, time.Now().UTC())
		if err != nil && !errors.Is(err, session.ErrSessionEnded) {
			require.NoError(t, err)
		}
	}
	target := run.Status
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	run.StartedAt = run.StartedAt.Truncate(time.Millisecond)
	start := session.RunStart{
		AgentID: run.AgentID, RunID: run.RunID, SessionID: run.SessionID,
		ParentRunID: run.ParentRunID, PredecessorRunID: predecessorRunID,
		StartedAt: run.StartedAt, Labels: run.Labels,
	}
	started := testHookRecord(t, hooks.NewRunStartedEvent(
		run.RunID,
		agent.Ident(run.AgentID),
		run.SessionID,
		run.ParentRunID,
		predecessorRunID,
		run.Labels,
	), "start", run.StartedAt)
	canceled := testHookRecord(t, hooks.NewRunCompletedEvent(
		run.RunID,
		agent.Ident(run.AgentID),
		run.SessionID,
		"canceled",
		agentrun.PhaseCanceled,
		run.Labels,
		context.Canceled,
		&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
	), terminalRunEventKey, run.StartedAt)
	var err error
	switch {
	case run.SessionID == "":
		_, err = store.StartOneShotRun(context.Background(), storage.OneShotRunStart{Run: start, Started: started})
	case start.ParentRunID == "":
		_, err = store.StartRootRun(context.Background(), storage.RootRunStart{Run: start, Started: started, Canceled: canceled})
	default:
		parent, loadErr := store.LoadRun(context.Background(), run.ParentRunID)
		require.NoError(t, loadErr)
		linked := testHookRecord(t, hooks.NewChildRunLinkedEvent(
			run.ParentRunID,
			agent.Ident(parent.AgentID),
			run.SessionID,
			"test.child",
			"call-"+run.RunID,
			run.RunID,
			agent.Ident(run.AgentID),
		), "child-link-"+run.RunID, run.StartedAt)
		_, err = store.StartChildRun(context.Background(), storage.ChildRunStart{Run: start, ParentLinked: linked, Started: started, Canceled: canceled})
	}
	require.NoError(t, err)
	switch target {
	case session.RunStatusRunning:
	case session.RunStatusSuspended, session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled:
		if target == session.RunStatusSuspended {
			suspended := testHookRecord(t, hooks.NewRunSuspendedEvent(
				run.RunID,
				agent.Ident(run.AgentID),
				run.SessionID,
				"test",
				"v6",
				1,
				nil,
			), terminalRunEventKey, run.StartedAt)
			_, err = store.RecordRunSuspension(context.Background(), storage.RunSuspension{
				RunID: run.RunID, Suspension: session.RunSuspension{ID: "test", Data: []byte(`{}`)},
				Record: suspended,
			})
		} else {
			status := "success"
			phase := agentrun.PhaseCompleted
			var terminalErr error
			var cancellation *agentrun.Cancellation
			if target == session.RunStatusFailed {
				status = "failed"
				phase = agentrun.PhaseFailed
				terminalErr = errors.New("failed")
			}
			if target == session.RunStatusCanceled {
				status = "canceled"
				phase = agentrun.PhaseCanceled
				terminalErr = context.Canceled
				cancellation = &agentrun.Cancellation{Reason: agentrun.CancellationReasonEngineCanceled}
			}
			terminal := testHookRecord(t, hooks.NewRunCompletedEvent(
				run.RunID,
				agent.Ident(run.AgentID),
				run.SessionID,
				status,
				phase,
				run.Labels,
				terminalErr,
				cancellation,
			), terminalRunEventKey, run.StartedAt)
			_, err = store.RecordRunTerminal(context.Background(), storage.RunTerminal{
				RunID: run.RunID, Status: target, Record: terminal,
			})
		}
		require.NoError(t, err)
	default:
		t.Fatalf("unsupported test run status %q", target)
	}
}

// testHookRecord encodes a lifecycle hook with the same codec used by
// workflow storage activities.
func testHookRecord(t testing.TB, event hooks.Event, key string, at time.Time) *runlog.Event {
	t.Helper()
	record, err := encodeTestHookRecord(event, key, at)
	require.NoError(t, err)
	return record
}

// encodeTestHookRecord converts a typed hook into its durable storage record.
func encodeTestHookRecord(event hooks.Event, key string, at time.Time) (*runlog.Event, error) {
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{EventKey: key, TimestampMS: at.UnixMilli()})
	if err != nil {
		return nil, err
	}
	return &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   input.Payload,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	}, nil
}

// outputContractCause requires the public output-contract category and returns
// its private validation cause for assertions that must not parse Error text.
func outputContractCause(t testing.TB, err error) error {
	t.Helper()
	var validationErr *model.OutputValidationError
	if errors.As(err, &validationErr) {
		return errors.Unwrap(validationErr)
	}
	var contractErr *outputcontract.Error
	require.ErrorAs(t, err, &contractErr)
	cause := errors.Unwrap(contractErr)
	for errors.As(cause, &contractErr) {
		cause = errors.Unwrap(contractErr)
	}
	return cause
}

// mustRuntimeToolInput compiles a static test schema.
func mustRuntimeToolInput(schema []byte) model.ToolInput {
	input, err := model.AdvertisedToolInputFromSchema(schema)
	if err != nil {
		panic(err)
	}
	return input
}

const testPublicationBatchID = "7b62faf2-1667-4f54-a807-46d151764717"

func testToolFailure(kind planner.FailureKind, action planner.RecoveryAction, message string) *planner.ToolFailure {
	var priorInput rawjson.Message
	if action == planner.RecoveryCorrectCall {
		priorInput = rawjson.Message(`{"invalid":true}`)
	}
	return &planner.ToolFailure{
		Kind:  kind,
		Error: planner.NewToolError(message),
		Recovery: planner.RecoveryDirective{
			Action:     action,
			PriorInput: priorInput,
		},
	}
}

func wrapExecute(fn func(context.Context, *ToolCall) (*planner.ToolResult, error)) func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
	return func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
		result, err := fn(ctx, call)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, nil
		}
		return Executed(result), nil
	}
}

// runLoop seeds workflow state directly for focused runtime tests. Callers
// provide the complete active cap state that the production workflow already
// materializes before entering the loop.
func (r *Runtime) runLoop(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	initialResult *PlanResult,
	caps policy.CapsState,
	budgetDeadline time.Time,
	hardDeadline time.Time,
	turnID string,
	_ any,
) (*RunOutput, error) {
	st := newRunLoopState(initialResult, nil, model.TokenUsage{}, caps, 2)
	return r.runLoopWithState(
		wfCtx,
		reg,
		input,
		base,
		st,
		budgetDeadline,
		hardDeadline,
		turnID,
		nil,
	)
}

func seedTestToolSpecs(rt *Runtime, specs ...tools.ToolSpec) {
	if rt.toolSpecs == nil {
		rt.toolSpecs = make(map[tools.Ident]tools.ToolSpec)
	}
	if rt.policyToolMetadata == nil {
		rt.policyToolMetadata = make(map[tools.Ident]policy.ToolMetadata)
	}
	for _, spec := range specs {
		rt.toolSpecs[spec.Name] = spec
		rt.policyToolMetadata[spec.Name] = canonicalToolMetadata(spec, nil)
	}
	for id, registration := range rt.agents {
		if !registration.Definition.valid() {
			registration.Definition = testAgentDefinition(
				id,
				string(id)+".workflow",
				"test",
				specs,
				nil)
		} else if len(registration.Definition.specs) == 0 && len(specs) > 0 {
			route := registration.Definition.route
			registration.Definition = testAgentDefinition(
				id, route.WorkflowName, route.DefaultTaskQueue,
				specs, registration.Definition.requiredLabels)
		}
		rt.agents[id] = registration
	}
}

func testAgentDefinition(
	id agent.Ident,
	workflow, queue string,
	specs []tools.ToolSpec,
	requiredLabels []string,
) AgentDefinition {
	return testAgentDefinitionWithChildren(id, workflow, queue, specs, requiredLabels, nil)
}

// testAgentDefinitionWithChildren builds one generated-style definition graph.
func testAgentDefinitionWithChildren(
	id agent.Ident,
	workflow, queue string,
	specs []tools.ToolSpec,
	requiredLabels []string,
	children []AgentDefinition,
) AgentDefinition {
	return NewAgentDefinition(
		AgentRoute{ID: id, WorkflowName: workflow, DefaultTaskQueue: queue},
		specs,
		nil,
		requiredLabels,
		nil,
		children,
	)
}

// testRegistrationDefinition converts the route and generated facts used by
// older handwritten fixtures into the same immutable definition codegen emits.
func testRegistrationDefinition(
	id agent.Ident,
	workflow engine.WorkflowDefinition,
	specs []tools.ToolSpec,
) AgentDefinition {
	if id == "" {
		id = "test.agent"
	}
	if workflow.Name == "" {
		workflow.Name = string(id) + ".workflow"
	}
	if workflow.TaskQueue == "" {
		workflow.TaskQueue = "test"
	}
	return testAgentDefinition(id, workflow.Name, workflow.TaskQueue, specs, nil)
}

func testRuntimeDefinition(rt *Runtime, id agent.Ident) AgentDefinition {
	registration := rt.agents[id]
	if registration.Definition.valid() {
		return registration.Definition
	}
	specs := make([]tools.ToolSpec, 0, len(rt.toolSpecs))
	for _, spec := range rt.toolSpecs {
		specs = append(specs, spec)
	}
	return testAgentDefinition(id, string(id)+".workflow", "test", specs, nil)
}

func (r *Runtime) ValidateContinuation(suspension *api.RunSuspension) error {
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return continuationContractError(err)
	}
	_, err = decodeWorkflowCheckpoint(suspension, testRuntimeDefinition(r, agent.Ident(checkpoint.AgentID)))
	if err != nil {
		return continuationContractError(err)
	}
	return nil
}

func (r *Runtime) decodeWorkflowCheckpoint(suspension *api.RunSuspension) (*workflowCheckpoint, error) {
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return nil, err
	}
	return decodeWorkflowCheckpoint(suspension, testRuntimeDefinition(r, agent.Ident(checkpoint.AgentID)))
}

func (r *Runtime) validateCheckpointToolOutput(ctx context.Context, output *planner.ToolOutput) error {
	_ = ctx
	return validateCheckpointToolOutput(output, testRuntimeDefinition(r, "svc.agent"))
}

func (r *Runtime) startRunOn(
	ctx context.Context,
	input *RunInput,
	workflow, queue string,
	requireSession bool,
) (engine.WorkflowHandle, error) {
	id := agent.Ident("")
	if input != nil {
		id = input.AgentID
	}
	definition := testRuntimeDefinition(r, id)
	definition.route.WorkflowName = workflow
	definition.route.DefaultTaskQueue = queue
	return r.startRunWithDefinition(ctx, input, definition, requireSession)
}

// seedTestToolset records the local registration that executes the supplied
// contracts in tests that build Runtime values directly.
func seedTestToolset(rt *Runtime, name string, specs ...tools.ToolSpec) {
	seedTestToolSpecs(rt, specs...)
	if rt.toolsetNames == nil {
		rt.toolsetNames = make(map[tools.Ident]string)
	}
	for _, spec := range specs {
		rt.toolsetNames[spec.Name] = name
	}
}

func testModelRequest(toolNames ...string) *model.Request {
	definitions := make([]*model.ToolDefinition, len(toolNames))
	for index, name := range toolNames {
		definitions[index] = &model.ToolDefinition{
			Name:  name,
			Input: mustRuntimeToolInput([]byte(`{"type":"object"}`)),
		}
	}
	return &model.Request{Tools: definitions}
}

func testModelResponse(content []model.Message, calls ...model.ToolCall) *model.Response {
	response := &model.Response{
		Content:    append([]model.Message(nil), content...),
		StopReason: "end_turn",
	}
	if len(calls) == 0 {
		return response
	}
	response.StopReason = "tool_use"
	if len(response.Content) == 0 {
		response.Content = append(response.Content, model.Message{Role: model.ConversationRoleAssistant})
	}
	message := &response.Content[len(response.Content)-1]
	for _, call := range calls {
		message.Parts = append(message.Parts, model.ToolUsePart{
			ID:               call.ID,
			Name:             string(call.Name),
			Input:            call.Payload,
			ThoughtSignature: call.ThoughtSignature,
		})
	}
	return response
}

// testModelResponseWithUsage builds a canonical response whose terminal usage
// agrees with the deltas emitted by its test stream.
func testModelResponseWithUsage(
	content []model.Message,
	usage model.TokenUsage,
	calls ...model.ToolCall,
) *model.Response {
	response := testModelResponse(content, calls...)
	response.Usage = usage
	return response
}

// testAppendCommand wraps ordinary records in the explicit append command.
func testAppendCommand(records ...*RecordActivityInput) *api.StorageActivityCommand {
	return appendStorageCommand(records...)
}

// testRecordBatch keeps ordinary-record tests concise while exercising the
// explicit append branch.
func testRecordBatch(records ...*RecordActivityInput) *api.StorageActivityCommand {
	return testAppendCommand(records...)
}

// recordActivity applies an explicit command when a test does not inspect the
// tagged result.
func (r *Runtime) recordActivity(ctx context.Context, command *api.StorageActivityCommand) error {
	_, err := r.executeStorageCommand(ctx, command)
	return err
}

// testWorkflowContext is a lightweight engine.WorkflowContext implementation used by tests.
type testWorkflowContext struct {
	ctx context.Context
	now func() time.Time

	lastHookCall       engine.StorageActivityCall
	lastPlannerCall    engine.PlannerActivityCall
	lastToolCall       engine.ToolActivityCall
	lastAgentChildCall engine.AgentChildActivityCall
	agentChildOutput   *api.AgentChildActivityOutput
	agentChildCalls    int

	asyncResult  ToolOutput
	sequenceMu   sync.Mutex
	nextSequence uint64
	workflowID   string

	cancellationHandler engine.CancellationHandler

	planResult      *PlanResult
	hasPlanResult   bool
	recoveryCatalog *RecoveryCatalog
	barrier         chan struct{}
	hookRuntime     *Runtime // optional runtime for storage activity execution
	runtime         *Runtime // optional runtime for activity execution (plan/resume/execute)
	childRuntime    *Runtime // optional runtime for child workflow execution

	childRequests      []engine.ChildWorkflowRequest
	firstChildGetCount int
	sawFirstChildGet   bool

	toolFutures map[string]*controlledToolFuture

	controlledChildHandles chan *controlledChildHandle

	// parent points to the original context when this is a derived context from WithCancel.
	// Test assertions can use the parent to inspect lastToolCall even when the call was
	// scheduled on a child context.
	parent *testWorkflowContext
}

func (t *testWorkflowContext) root() *testWorkflowContext {
	if t.parent != nil {
		return t.parent
	}
	return t
}

func (t *testWorkflowContext) Context() context.Context {
	if t.ctx == nil {
		panic("testWorkflowContext.ctx is nil")
	}
	return engine.WithWorkflowContext(t.ctx, t)
}

func (t *testWorkflowContext) WorkflowID() string {
	if t.workflowID != "" {
		return t.workflowID
	}
	return "wf"
}

func (t *testWorkflowContext) RunID() string {
	return "run"
}

func (t *testWorkflowContext) Detached() engine.WorkflowContext {
	if t.ctx == nil {
		panic("testWorkflowContext.ctx is nil")
	}
	cctx := context.WithoutCancel(t.ctx)
	root := t.root()
	sub := &testWorkflowContext{
		ctx: cctx,

		lastHookCall:    t.lastHookCall,
		lastPlannerCall: t.lastPlannerCall,
		lastToolCall:    t.lastToolCall,

		asyncResult: t.asyncResult,
		workflowID:  t.workflowID,
		now:         t.now,

		planResult:      t.planResult,
		hasPlanResult:   t.hasPlanResult,
		recoveryCatalog: t.recoveryCatalog,
		barrier:         t.barrier,
		hookRuntime:     t.hookRuntime,
		runtime:         t.runtime,
		childRuntime:    t.childRuntime,

		childRequests:      t.childRequests,
		firstChildGetCount: t.firstChildGetCount,
		sawFirstChildGet:   t.sawFirstChildGet,

		toolFutures: t.toolFutures,

		controlledChildHandles: t.controlledChildHandles,
		parent:                 root,
	}
	return sub
}

func (t *testWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	if t.ctx == nil {
		panic("testWorkflowContext.ctx is nil")
	}
	cctx, cancel := context.WithCancel(t.ctx)
	root := t.root()
	sub := &testWorkflowContext{
		ctx: cctx,

		lastHookCall:    t.lastHookCall,
		lastPlannerCall: t.lastPlannerCall,
		lastToolCall:    t.lastToolCall,

		asyncResult: t.asyncResult,
		workflowID:  t.workflowID,
		now:         t.now,

		planResult:      t.planResult,
		hasPlanResult:   t.hasPlanResult,
		recoveryCatalog: t.recoveryCatalog,
		barrier:         t.barrier,
		hookRuntime:     t.hookRuntime,
		runtime:         t.runtime,
		childRuntime:    t.childRuntime,

		childRequests:      t.childRequests,
		firstChildGetCount: t.firstChildGetCount,
		sawFirstChildGet:   t.sawFirstChildGet,

		toolFutures:            t.toolFutures,
		controlledChildHandles: t.controlledChildHandles,
		parent:                 root,
	}
	return sub, cancel
}

func (t *testWorkflowContext) Now() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Unix(0, 0)
}

func (t *testWorkflowContext) NextSequence() uint64 {
	root := t.root()
	root.sequenceMu.Lock()
	defer root.sequenceMu.Unlock()
	root.nextSequence++
	return root.nextSequence
}

func (t *testWorkflowContext) NewTimer(ctx context.Context, d time.Duration) (engine.Future[time.Time], error) {
	now := time.Now()
	if d <= 0 {
		fut := &controlledTimeFuture{ready: make(chan struct{}), v: now}
		close(fut.ready)
		return fut, nil
	}
	fireAt := now.Add(d)
	fut := &controlledTimeFuture{ready: make(chan struct{}), v: fireAt}
	go func() {
		defer close(fut.ready)
		select {
		case <-ctx.Done():
			fut.err = ctx.Err()
		case <-time.After(d):
		}
	}()
	return fut, nil
}

func (t *testWorkflowContext) Await(condition func() bool) error {
	if condition == nil {
		return fmt.Errorf("await condition is required")
	}
	ticker := time.NewTicker(1 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return nil
		}
		select {
		case <-t.ctx.Done():
			return t.ctx.Err()
		case <-ticker.C:
		}
	}
}

func (t *testWorkflowContext) SetQueryHandler(name string, handler any) error {
	return nil
}

func (t *testWorkflowContext) SetCancellationHandler(handler engine.CancellationHandler) error {
	t.root().cancellationHandler = handler
	return nil
}

func (t *testWorkflowContext) StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	t.childRequests = append(t.childRequests, req)
	// Also update parent if this is a derived context so tests can track from root.
	if t.parent != nil {
		t.parent.childRequests = append(t.parent.childRequests, req)
	}
	if t.controlledChildHandles != nil {
		h := &controlledChildHandle{
			ready: make(chan struct{}),
			out: &api.RunOutput{
				AgentID: req.Input.AgentID,
				RunID:   req.Input.RunID,
				Final: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "completed"}},
				},
			},
		}
		t.controlledChildHandles <- h
		return h, nil
	}
	childRT := t.childRuntime
	if childRT == nil {
		childRT = t.runtime
	}
	return &testChildHandle{
		runtime: childRT,
		request: req,
		wfCtx:   t,
	}, nil
}

func (t *testWorkflowContext) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	t.lastHookCall = call
	hookRT := t.hookRuntime
	if hookRT == nil {
		hookRT = t.runtime
	}
	if hookRT == nil {
		return testStorageResult(call.Command), nil
	}
	if call.Name != storageActivityName {
		return nil, fmt.Errorf("unexpected storage activity name %q", call.Name)
	}
	return hookRT.executeStorageCommand(t.Context(), call.Command)
}

// testStorageResult returns a valid result branch for workflow tests that do
// not need a real Store.
func testStorageResult(command *api.StorageActivityCommand) *api.StorageActivityResult {
	switch {
	case command.Append != nil:
		return &api.StorageActivityResult{Append: &api.AppendRecordsResult{
			Records: make([]storage.AppendResult, len(command.Append.Records)),
		}}
	case command.RootStart != nil:
		return &api.StorageActivityResult{RootStart: &api.StartRunResult{Outcome: session.RunStartProceed}}
	case command.ChildStart != nil:
		return &api.StorageActivityResult{ChildStart: &api.StartRunResult{Outcome: session.RunStartProceed}}
	case command.OneShotStart != nil:
		return &api.StorageActivityResult{OneShotStart: &api.StartRunResult{Outcome: session.RunStartProceed}}
	case command.OneShotChildStart != nil:
		return &api.StorageActivityResult{OneShotChildStart: &api.StartRunResult{Outcome: session.RunStartProceed}}
	case command.Cancellation != nil:
		return &api.StorageActivityResult{Cancellation: &api.RunCancellationResult{Outcome: api.RunCancellationAccepted}}
	case command.Suspension != nil:
		return &api.StorageActivityResult{Suspension: &api.RecordWriteResult{}}
	default:
		return &api.StorageActivityResult{Terminal: &api.RecordWriteResult{}}
	}
}

func (t *testWorkflowContext) ExecutePlannerActivity(call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	t.lastPlannerCall = call
	switch call.Name {
	case "plan", "nested.plan":
		if t.runtime != nil {
			return t.runtime.PlanStartActivity(t.Context(), call.Input)
		}
	case "resume", "nested.resume":
		if t.runtime != nil {
			return t.runtime.PlanResumeActivity(t.Context(), call.Input)
		}
	}

	var result *PlanResult
	if t.hasPlanResult {
		result = t.planResult
	}
	return &PlanActivityOutput{
		PublicationBatchID: uuid.NewString(),
		Result:             result,
		Transcript:         nil,
		RecoveryCatalog:    t.recoveryCatalog,
	}, nil
}

func (t *testWorkflowContext) ExecuteToolActivity(call engine.ToolActivityCall) (*api.ToolOutput, error) {
	fut, err := t.ExecuteToolActivityAsync(call)
	if err != nil {
		return nil, err
	}
	return fut.Get(t.Context())
}

func (t *testWorkflowContext) ExecuteToolActivityAsync(call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	t.lastToolCall = call
	// Also update parent if this is a derived context, so tests can inspect from the root.
	if t.parent != nil {
		t.parent.lastToolCall = call
	}

	if call.Input != nil && call.Input.ToolCallID != "" && len(t.toolFutures) > 0 {
		if fut, ok := t.toolFutures[call.Input.ToolCallID]; ok && fut != nil {
			return fut, nil
		}
	}

	fut := &testToolFuture{
		barrier: t.barrier,
	}

	switch call.Name {
	case "execute", "nested.execute":
		if t.runtime != nil {
			fut.result, fut.err = t.runtime.ExecuteToolActivity(t.Context(), call.Input)
			return fut, nil
		}
	}

	result := t.asyncResult
	fut.result = &result
	return fut, nil
}

func (t *testWorkflowContext) ExecuteAgentChildActivity(call engine.AgentChildActivityCall) (*api.AgentChildActivityOutput, error) {
	t.lastAgentChildCall = call
	t.agentChildCalls++
	if t.agentChildOutput != nil {
		return t.agentChildOutput, nil
	}
	activityRuntime := t.runtime
	if activityRuntime == nil {
		activityRuntime = t.hookRuntime
	}
	if activityRuntime == nil {
		return nil, errors.New("agent child activity runtime is required")
	}
	return activityRuntime.prepareAgentChildActivity(t.Context(), call.Input)
}

type controlledTimeFuture struct {
	ready chan struct{}
	v     time.Time
	err   error
}

func (f *controlledTimeFuture) Get(ctx context.Context) (time.Time, error) {
	select {
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	case <-f.ready:
		return f.v, f.err
	}
}

func (f *controlledTimeFuture) IsReady() bool {
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

type testToolFuture struct {
	result  *api.ToolOutput
	err     error
	barrier chan struct{}
}

func (f *testToolFuture) Get(ctx context.Context) (*api.ToolOutput, error) {
	if f.barrier != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.barrier:
		}
	}
	return f.result, f.err
}

func (f *testToolFuture) IsReady() bool {
	return true
}

type controlledToolFuture struct {
	ready chan struct{}
	out   *api.ToolOutput
	err   error
}

func (f *controlledToolFuture) Get(ctx context.Context) (*api.ToolOutput, error) {
	if f == nil {
		return nil, fmt.Errorf("nil future")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.ready:
		return f.out, f.err
	}
}

func (f *controlledToolFuture) IsReady() bool {
	if f == nil {
		return true
	}
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

type controlledChildHandle struct {
	ready chan struct{}
	out   *api.RunOutput
	err   error

	mu       sync.Mutex
	canceled bool
}

func (h *controlledChildHandle) Get(ctx context.Context) (*api.RunOutput, error) {
	if h == nil {
		return nil, fmt.Errorf("nil child handle")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.ready:
		return h.out, h.err
	}
}

func (h *controlledChildHandle) IsReady() bool {
	if h == nil {
		return true
	}
	select {
	case <-h.ready:
		return true
	default:
		return false
	}
}

func (h *controlledChildHandle) Cancel(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.canceled = true
	return nil
}

func (h *controlledChildHandle) wasCanceled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.canceled
}

type stubPlanner struct {
	start  func(context.Context, *planner.PlanInput) (*planner.PlanResult, error)
	resume func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error)
}

func (s *stubPlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	if s.start != nil {
		return s.start(ctx, input)
	}
	return &planner.PlanResult{}, nil
}

func (s *stubPlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if s.resume != nil {
		return s.resume(ctx, input)
	}
	return &planner.PlanResult{}, nil
}

type stubEngine struct {
	last                             engine.WorkflowStartRequest
	startCalls                       int
	registeredWorkflow               engine.WorkflowDefinition
	registeredStorageActivityOptions map[string]engine.ActivityOptions
	registeredPlannerActivityOptions map[string]engine.ActivityOptions
	registeredExecuteActivityOptions map[string]engine.ActivityOptions
	registeredAgentChildOptions      map[string]engine.ActivityOptions
	sealCalls                        int
	sealErrors                       []error
}

func (s *stubEngine) RegisterWorkflow(_ context.Context, definition engine.WorkflowDefinition) error {
	s.registeredWorkflow = definition
	return nil
}
func (s *stubEngine) RegisterStorageActivity(_ context.Context, name string, opts engine.ActivityOptions, _ func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error)) error {
	if s.registeredStorageActivityOptions == nil {
		s.registeredStorageActivityOptions = make(map[string]engine.ActivityOptions)
	}
	s.registeredStorageActivityOptions[name] = opts
	return nil
}
func (s *stubEngine) RegisterPlannerActivity(_ context.Context, name string, opts engine.ActivityOptions, _ func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	if s.registeredPlannerActivityOptions == nil {
		s.registeredPlannerActivityOptions = make(map[string]engine.ActivityOptions)
	}
	s.registeredPlannerActivityOptions[name] = opts
	return nil
}
func (s *stubEngine) RegisterExecuteToolActivity(_ context.Context, name string, opts engine.ActivityOptions, _ func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	if s.registeredExecuteActivityOptions == nil {
		s.registeredExecuteActivityOptions = make(map[string]engine.ActivityOptions)
	}
	s.registeredExecuteActivityOptions[name] = opts
	return nil
}
func (s *stubEngine) RegisterAgentChildActivity(_ context.Context, name string, opts engine.ActivityOptions, _ func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error)) error {
	if s.registeredAgentChildOptions == nil {
		s.registeredAgentChildOptions = make(map[string]engine.ActivityOptions)
	}
	s.registeredAgentChildOptions[name] = opts
	return nil
}
func (s *stubEngine) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	s.startCalls++
	s.last = req
	return noopWorkflowHandle{}, nil
}

func (s *stubEngine) QueryRunCompletion(context.Context, string) (engine.RunCompletion, error) {
	return engine.RunCompletion{
		Status: engine.RunStatusCompleted, CompletedAt: time.Now().UTC(), Output: &api.RunOutput{},
	}, nil
}

func (s *stubEngine) SealRegistration(context.Context) error {
	s.sealCalls++
	if len(s.sealErrors) == 0 {
		return nil
	}
	idx := s.sealCalls - 1
	if idx >= len(s.sealErrors) {
		idx = len(s.sealErrors) - 1
	}
	if s.sealErrors[idx] == nil {
		return nil
	}
	return s.sealErrors[idx]
}

type noopWorkflowHandle struct{}

func (noopWorkflowHandle) Wait(context.Context) (*api.RunOutput, error) { return &api.RunOutput{}, nil }
func (noopWorkflowHandle) Cancel(context.Context) error                 { return nil }

func newTestRuntimeWithPlanner(agentID agent.Ident, pl planner.Planner) *Runtime {
	return &Runtime{
		agents:    map[agent.Ident]AgentRegistration{agentID: {Planner: pl}},
		toolsets:  make(map[string]ToolsetRegistration),
		toolSpecs: make(map[tools.Ident]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Store:     newTestStore(),
		Bus:       noopHooks{},
		models:    make(map[string]model.Client),
	}
}

type recordingHooks struct {
	mu     sync.Mutex
	events []hooks.Event
	ch     chan hooks.Event
}

func (r *recordingHooks) Publish(ctx context.Context, event hooks.Event) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	if r.ch != nil {
		r.ch <- event
	}
	return nil
}

func (r *recordingHooks) Register(h hooks.Subscriber) (hooks.Subscription, error) {
	return noopSubscription{}, nil
}

// countRunEventsByType returns the number of canonical events of one type in a
// run-log page so suspension tests can prove prompt ownership by run ID.
func countRunEventsByType(page runlog.Page, eventType runlog.Type) int {
	count := 0
	for _, event := range page.Events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

type noopHooks struct{}

func (noopHooks) Publish(context.Context, hooks.Event) error { return nil }
func (noopHooks) Register(hooks.Subscriber) (hooks.Subscription, error) {
	return noopSubscription{}, nil
}

type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }

type stubPolicyEngine struct{ decision policy.Decision }

func (s *stubPolicyEngine) Decide(context.Context, policy.Input) (policy.Decision, error) {
	return s.decision, nil
}

type testChildHandle struct {
	runtime *Runtime
	request engine.ChildWorkflowRequest
	wfCtx   engine.WorkflowContext
}

func (h *testChildHandle) Get(ctx context.Context) (*api.RunOutput, error) {
	if tw, ok := h.wfCtx.(*testWorkflowContext); ok {
		if !tw.sawFirstChildGet {
			tw.sawFirstChildGet = true
			tw.firstChildGetCount = len(tw.childRequests)
		}
		// Also update parent if this is a derived context.
		if tw.parent != nil && !tw.parent.sawFirstChildGet {
			tw.parent.sawFirstChildGet = true
			tw.parent.firstChildGetCount = len(tw.childRequests)
		}
	}
	if h.runtime != nil && h.request.Input != nil {
		// Execute the nested agent workflow
		return h.runtime.ExecuteWorkflow(h.wfCtx, h.request.Input)
	}
	return &api.RunOutput{
		AgentID: h.request.Input.AgentID,
		RunID:   h.request.Input.RunID,
		Final: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "completed"}},
		},
	}, nil
}

func (h *testChildHandle) IsReady() bool {
	// Test child handles complete synchronously when Get is invoked.
	// We return true so callers can treat them as ready when draining.
	return true
}
func (h *testChildHandle) Cancel(ctx context.Context) error { return nil }

func newAnyJSONSpec(name tools.Ident) tools.ToolSpec {
	codec := tools.JSONCodec[any]{
		ToJSON: json.Marshal,
		FromJSON: func(data []byte) (any, error) {
			if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
				return nil, nil
			}
			var out any
			if err := json.Unmarshal(data, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	}
	return tools.ToolSpec{
		Name:    name,
		Payload: tools.TypeSpec{Name: string(name) + "_payload", Codec: codec},
		Result:  tools.TypeSpec{Name: string(name + "_result"), Codec: codec},
	}
}
