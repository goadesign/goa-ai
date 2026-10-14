// A direct parent keeps ownership of a suspended child while another child is
// running. Cancellation must remain accepted and settle both saved and live work.
package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// observedCancellationEngine delays job delivery after durable acceptance and
	// records the receiving worker separately from the workflow receiving an update.
	observedCancellationEngine struct {
		engine.Engine
		mu           sync.Mutex
		activeOwners []string
		jobNames     []string
		jobErrors    []error
		receivers    []string
		deliveryGate chan struct{}
		beforeActive func()
	}

	// settlingParentReadStore finishes another cancellation immediately before
	// returning one parent read, leaving the caller with its older child snapshot.
	settlingParentReadStore struct {
		*testStore
		mu         sync.Mutex
		parentID   string
		beforeRead func()
	}
)

func TestDirectRunningParentOwnsSuspendedChildCancellation(t *testing.T) {
	for _, activity := range []bool{false, true} {
		for _, root := range []bool{false, true} {
			name := "child sibling"
			if activity {
				name = "activity sibling"
			}
			if root {
				name += "/cancel root"
			} else {
				name += "/cancel child"
			}
			t.Run(name, func(t *testing.T) { testRunningParentCancellation(t, activity, root, false, "") })
		}
	}
}

func TestNestedRunningParentCancellationKeepsOuterJobOwner(t *testing.T) {
	for _, delay := range []string{"", "delivery", "admission"} {
		name := "nearest running provider"
		if delay != "" {
			name = "both parents suspend before " + delay
		}
		t.Run(name, func(t *testing.T) { testRunningParentCancellation(t, false, false, true, delay) })
	}
}

func TestDirectCancellationOwnerSettlesBeforeJobAdmission(t *testing.T) {
	testRunningParentCancellation(t, false, false, false, "settlement")
}

func TestDirectCancellationOwnerSettlesBeforeParentRead(t *testing.T) {
	for _, delay := range []string{"owner admission", "owner observation"} {
		t.Run(delay, func(t *testing.T) { testRunningParentCancellation(t, false, false, false, delay) })
	}
}

func testRunningParentCancellation(t *testing.T, activitySibling, cancelRoot, nested bool, delay string) {
	resolution := testNativeRegistryResolution("revision/1")
	declaration := resolution.Toolset.Tools[0]
	resolved, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	exported := resolved.Specs[tools.Ident(declaration.Name)]
	questionTool, blockedTool := newAnyJSONSpec("private.question.ask"), newAnyJSONSpec("private.blocked.wait")
	questionTool.IsAgentTool, questionTool.AgentID = true, "question.agent"
	blockedTool.IsAgentTool, blockedTool.AgentID = true, "blocked.agent"
	completedTool := newAnyJSONSpec("private.completed.read")
	completedTool.IsAgentTool, completedTool.AgentID = true, "completed.agent"
	completedDefinition := testAgentDefinition("completed.agent", "completed.workflow", "completed.queue", []tools.ToolSpec{completedTool}, nil)
	activityTool := newAnyJSONSpec("service.blocked.wait")
	questionDefinition := testAgentDefinition("question.agent", "question.workflow", "question.queue", []tools.ToolSpec{questionTool}, nil)
	blockedDefinition := testAgentDefinition("blocked.agent", "blocked.workflow", "blocked.queue", []tools.ToolSpec{blockedTool}, nil)
	root := NewAgentDefinition(AgentRoute{ID: "generic.agent", WorkflowName: "generic.workflow", DefaultTaskQueue: "generic.queue"}, []tools.ToolSpec{exported, questionTool, blockedTool, completedTool, activityTool}, nil, nil, []tools.Ident{questionTool.Name, blockedTool.Name, completedTool.Name, activityTool.Name}, []AgentDefinition{questionDefinition, blockedDefinition, completedDefinition}, nil)
	backend := &observedCancellationEngine{Engine: engineinmem.New()}
	if delay == "delivery" {
		backend.deliveryGate = make(chan struct{})
		defer func() {
			select {
			case <-backend.deliveryGate:
			default:
				close(backend.deliveryGate)
			}
		}()
	}
	store := &settlingParentReadStore{testStore: newTestStore()}
	rt := New(store, WithEngine(backend))
	outer := testRegistryAgentDefinition(testRegistrySources{})
	if nested {
		require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{ResolveToolsetEndpoint: func(context.Context, any) (any, error) { return resolution, nil }}, unusedRegistryPulse{}))
		require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
			Definition: outer, WorkflowHandler: rt.ExecuteWorkflow,
			PlanActivityName: "outer.plan", ResumeActivityName: "outer.resume", ExecuteToolActivity: "outer.execute",
			Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: exported.Name, Payload: rawjson.Message(`{"value":1}`)}}}, nil
			}},
		}))
	}
	for _, child := range []struct {
		definition AgentDefinition
		spec       tools.ToolSpec
		name       string
	}{
		{questionDefinition, questionTool, "private.question"}, {blockedDefinition, blockedTool, "private.blocked"}, {completedDefinition, completedTool, "private.completed"},
	} {
		registration := NewAgentToolsetRegistration(AgentToolConfig{
			Definition: child.definition, Name: child.name,
			AgentToolContent: AgentToolContent{Prompt: func(tools.Ident, any) string { return "Read a private record." }},
		})
		registration.Specs = []tools.ToolSpec{child.spec}
		require.NoError(t, rt.RegisterToolset(registration))
	}
	questionID, blockedID, completedID := make(chan string, 1), make(chan string, 1), make(chan string, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "service.blocked", Specs: []tools.ToolSpec{activityTool},
		Execute: func(ctx context.Context, _ *ToolCall) (*ToolExecutionResult, error) {
			blockedID <- "activity"
			select {
			case <-release:
				return Executed(&planner.ToolResult{Name: activityTool.Name, Result: map[string]any{}}), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}))
	for _, registration := range []AgentRegistration{
		{Definition: root, Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			blocker := blockedTool.Name
			if activitySibling {
				blocker = activityTool.Name
			}
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: completedTool.Name, Payload: rawjson.Message(`{}`)}, {Name: questionTool.Name, Payload: rawjson.Message(`{}`)}, {Name: blocker, Payload: rawjson.Message(`{}`)}}}, nil
		}}},
		{Definition: completedDefinition, Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			completedID <- input.RunContext.RunID
			return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"retained":true}`)}}, nil
		}}},
		{Definition: questionDefinition, Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			questionID <- input.RunContext.RunID
			return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "question", Question: "Which record?"}))}, nil
		}}},
		{Definition: blockedDefinition, Planner: &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			blockedID <- input.RunContext.RunID
			select {
			case <-release:
				return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{}`)}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}}},
	} {
		registration.WorkflowHandler = rt.ExecuteWorkflow
		registration.PlanActivityName = string(registration.Definition.route.ID) + ".plan"
		registration.ResumeActivityName = string(registration.Definition.route.ID) + ".resume"
		registration.ExecuteToolActivity = string(registration.Definition.route.ID) + ".execute"
		require.NoError(t, rt.RegisterAgent(t.Context(), registration))
	}
	require.NoError(t, rt.RegisterAgentToolResolver(root.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read both records."}}}})}, nil
	}))
	_, err = createSessionForTest(t.Context(), rt.Store, "session")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var parent engine.WorkflowHandle
	if nested {
		parent, err = rt.MustClientFor(outer).Start(ctx, "session", []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the selected records."}}}}, WithRunID("outer"))
	} else {
		parent, err = rt.StartOperation(ctx, "session", "parent", declaration, rawjson.Message(`{"value":1}`), nil, nil)
	}
	require.NoError(t, err)
	parentDone := make(chan error, 1)
	go func() { _, waitErr := parent.Wait(ctx); parentDone <- waitErr }()
	var questionRun, blockedRun string
	select {
	case questionRun = <-questionID:
	case err := <-parentDone:
		t.Fatalf("parent closed before question child started: %v", err)
	case <-ctx.Done():
		t.Fatal("question child did not start")
	}
	select {
	case blockedRun = <-blockedID:
	case <-ctx.Done():
		t.Fatal("blocked child did not start")
	}
	require.Eventually(t, func() bool {
		meta, loadErr := rt.Store.LoadRun(ctx, questionRun)
		return loadErr == nil && meta.Status == session.RunStatusSuspended
	}, 3*time.Second, time.Millisecond)
	var completedRun string
	select {
	case completedRun = <-completedID:
	case <-ctx.Done():
		t.Fatal("completed child did not start")
	}
	require.Eventually(t, func() bool {
		completion, err := rt.Engine.QueryRunCompletion(ctx, completedRun)
		return err == nil && completion.Status == engine.RunStatusCompleted
	}, 3*time.Second, time.Millisecond)
	questionMeta, err := rt.Store.LoadRun(ctx, questionRun)
	require.NoError(t, err)
	ownerID := questionMeta.ParentRunID
	meta, err := rt.Store.LoadRun(ctx, ownerID)
	require.NoError(t, err)
	require.Equal(t, session.RunStatusRunning, meta.Status)
	unrelated := engine.CancellationRequest{RunID: "unrelated", Reason: run.CancellationReasonUserRequested}
	require.ErrorIs(t, rt.Engine.(engine.CancellationRequester).RequestCancellation(ctx, ownerID, unrelated), engine.ErrCancellationRunNotOwned)
	selected := questionRun
	if cancelRoot {
		selected = ownerID
	}
	if delay == "admission" {
		backend.beforeActive = func() {
			releaseOnce.Do(func() { close(release) })
			require.Eventually(t, func() bool {
				inner, err := rt.Store.LoadRun(ctx, ownerID)
				if err != nil || inner.Status != session.RunStatusSuspended {
					return false
				}
				outer, err := rt.Store.LoadRun(ctx, "outer")
				if err != nil || outer.Status != session.RunStatusSuspended {
					return false
				}
				completion, err := backend.QueryRunCompletion(ctx, "outer")
				return err == nil && completion.Status == engine.RunStatusCompleted
			}, 3*time.Second, time.Millisecond)
		}
	}
	if delay == "settlement" || delay == "owner admission" || delay == "owner observation" {
		settleOwner := func() {
			require.Eventually(t, func() bool {
				completion, err := backend.QueryRunCompletion(ctx, questionRun)
				return err == nil && completion.Status == engine.RunStatusCompleted
			}, 3*time.Second, time.Millisecond)
			require.NoError(t, backend.Engine.(engine.CancellationRequester).RequestCancellation(ctx, ownerID, engine.CancellationRequest{RunID: selected, Reason: run.CancellationReasonUserRequested}))
			require.Eventually(t, func() bool {
				completion, err := backend.QueryRunCompletion(ctx, ownerID)
				return err == nil && completion.Status == engine.RunStatusCanceled
			}, 3*time.Second, time.Millisecond)
		}
		if delay == "settlement" {
			backend.beforeActive = settleOwner
		} else {
			store.mu.Lock()
			store.parentID, store.beforeRead = ownerID, settleOwner
			store.mu.Unlock()
		}
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, id := range []string{questionRun, ownerID, "outer"} {
			meta, err := rt.Store.LoadRun(context.Background(), id)
			t.Logf("cancellation source %q: %+v; load error: %v", id, meta, err)
			if err == nil && meta.SuccessorRunID != "" {
				current, loadErr := rt.selectedCancellationRun(context.Background(), meta)
				completion, engineErr := backend.QueryRunCompletion(context.Background(), current.RunID)
				t.Logf("cancellation successor: %+v; load error: %v; engine: %+v; engine error: %v", current, loadErr, completion, engineErr)
			}
		}
		backend.mu.Lock()
		defer backend.mu.Unlock()
		t.Logf("cancellation owners: %v; jobs: %v; receivers: %v; job errors: %v", backend.activeOwners, backend.jobNames, backend.receivers, backend.jobErrors)
	})
	request := CancelRequest{RunID: selected, Reason: run.CancellationReasonUserRequested}
	if delay == "owner observation" {
		settled, err := rt.cancellationActivity(ctx, request)
		require.NoError(t, err)
		require.True(t, settled)
	} else {
		require.NoError(t, rt.CancelRun(ctx, request))
	}
	if delay == "owner admission" || delay == "owner observation" {
		store.mu.Lock()
		assert.Nil(t, store.beforeRead, "parent read must trigger the concurrent cancellation")
		store.mu.Unlock()
		backend.mu.Lock()
		assert.Empty(t, backend.activeOwners, "terminal parent must not admit an active cancellation job")
		assert.Empty(t, backend.jobNames, "settled target needs no further cleanup job")
		backend.mu.Unlock()
	}
	if delay == "delivery" {
		releaseOnce.Do(func() { close(release) })
		require.Eventually(t, func() bool {
			inner, err := rt.Store.LoadRun(ctx, ownerID)
			if err != nil || inner.Status != session.RunStatusSuspended {
				return false
			}
			outerMeta, err := rt.Store.LoadRun(ctx, "outer")
			return err == nil && outerMeta.Status == session.RunStatusSuspended
		}, 3*time.Second, time.Millisecond)
		close(backend.deliveryGate)
	}
	err = <-parentDone
	t.Logf("parent completion: %v", err)
	if err != nil {
		require.ErrorIs(t, err, context.Canceled)
	}
	require.Eventually(t, func() bool {
		source, loadErr := rt.Store.LoadRun(ctx, questionRun)
		if loadErr != nil || source.SuccessorRunID == "" {
			return false
		}
		successor, loadErr := rt.Store.LoadRun(ctx, source.SuccessorRunID)
		reason := run.CancellationReasonUserRequested
		if cancelRoot {
			reason = run.CancellationReasonEngineCanceled
		}
		return loadErr == nil && successor.Status == session.RunStatusCanceled && successor.CancellationReason == reason
	}, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		parentMeta, err := rt.Store.LoadRun(ctx, ownerID)
		if err != nil {
			return false
		}
		current, err := rt.selectedCancellationRun(ctx, parentMeta)
		return err == nil && current.Status == session.RunStatusCanceled
	}, 3*time.Second, time.Millisecond)
	if nested {
		require.Eventually(t, func() bool {
			rootMeta, err := rt.Store.LoadRun(ctx, "outer")
			if err != nil {
				return false
			}
			current, err := rt.selectedCancellationRun(ctx, rootMeta)
			return err == nil && current.Status == session.RunStatusCanceled
		}, 3*time.Second, time.Millisecond)
		backend.mu.Lock()
		assert.Equal(t, []string{"outer"}, backend.activeOwners)
		assert.NotEmpty(t, backend.jobNames)
		for _, name := range backend.jobNames {
			assert.Equal(t, "records.workflow.cancel", name)
		}
		if delay == "" {
			assert.Contains(t, backend.receivers, ownerID)
		}
		backend.mu.Unlock()
	}
	if !activitySibling {
		sibling, err := rt.Store.LoadRun(ctx, blockedRun)
		require.NoError(t, err)
		if delay == "admission" || delay == "delivery" {
			assert.Equal(t, session.RunStatusCompleted, sibling.Status)
		} else {
			assert.Equal(t, session.RunStatusCanceled, sibling.Status)
		}
	}
	completed, err := rt.Store.LoadRun(ctx, completedRun)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCompleted, completed.Status)
	assert.Empty(t, completed.SuccessorRunID)
	page, err := rt.ListRunEvents(ctx, ownerID, "", 100)
	require.NoError(t, err)
	retained := 0
	for _, event := range page.Events {
		if event.Type != hooks.ToolResultReceived {
			continue
		}
		decoded, err := hooks.DecodeRunlogEvent(event)
		require.NoError(t, err)
		result := decoded.(*hooks.ToolResultReceivedEvent)
		if result.ToolName == completedTool.Name {
			retained++
			assert.Nil(t, result.Failure)
			assert.JSONEq(t, `{"retained":true}`, string(result.ResultJSON))
		}
	}
	assert.Equal(t, 1, retained)
	require.NoError(t, rt.CancelRun(ctx, CancelRequest{RunID: selected, Reason: run.CancellationReasonUserRequested}))
	var conflict *CancellationReasonConflictError
	assert.ErrorAs(t, rt.CancelRun(ctx, CancelRequest{RunID: selected, Reason: run.CancellationReasonSessionEnded}), &conflict)
}

func (e *observedCancellationEngine) StartActiveCancellationWorkflow(ctx context.Context, id string, request engine.CancellationRequest) error {
	e.mu.Lock()
	e.activeOwners = append(e.activeOwners, id)
	e.mu.Unlock()
	if e.beforeActive != nil {
		e.beforeActive()
	}
	return e.Engine.StartActiveCancellationWorkflow(ctx, id, request)
}

func (e *observedCancellationEngine) RequestCancellation(ctx context.Context, id string, request engine.CancellationRequest) error {
	e.mu.Lock()
	e.receivers = append(e.receivers, id)
	e.mu.Unlock()
	return e.Engine.(engine.CancellationRequester).RequestCancellation(ctx, id, request)
}

func (e *observedCancellationEngine) RegisterCancellationWorkflow(ctx context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, engine.CancellationRequest) (bool, error)) error {
	return e.Engine.RegisterCancellationWorkflow(ctx, name, opts, func(ctx context.Context, request engine.CancellationRequest) (bool, error) {
		if e.deliveryGate != nil {
			select {
			case <-e.deliveryGate:
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		e.mu.Lock()
		e.jobNames = append(e.jobNames, name)
		e.mu.Unlock()
		settled, err := fn(ctx, request)
		if err != nil {
			e.mu.Lock()
			e.jobErrors = append(e.jobErrors, err)
			e.mu.Unlock()
		}
		return settled, err
	})
}

func (s *settlingParentReadStore) LoadRun(ctx context.Context, id string) (session.RunMeta, error) {
	s.mu.Lock()
	var before func()
	if id == s.parentID {
		before, s.beforeRead = s.beforeRead, nil
	}
	s.mu.Unlock()
	if before != nil {
		before()
	}
	return s.testStore.LoadRun(ctx, id)
}
