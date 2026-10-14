// Registered-root tests use synthetic provider declarations and verify identity
// without depending on a model, registered consuming Agent or private service.
package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/content"
)

func registeredRootFixture() (*genregistry.ResolvedToolset, *genregistry.RegisteredInvocation) {
	resolution := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	resolution.Toolset.Tools[0].ConsumerContract.Confirmation = nil
	request := &genregistry.RegisteredInvocation{Invocation: &genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "service-root"})}, Target: &genregistry.CatalogOperation{Scope: "provider", Name: "records", Target: genregistry.NewCatalogTargetService(&genregistry.ServiceCatalogTarget{Toolset: "provider.records", Tool: "records.find", RegistrationToken: strings.Repeat("a", 64)})}, Arguments: []byte(`{"value":9007199254740993}`)}
	return resolution, request
}

func TestRegisteredRootRequestRetainsExactSelection(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	runtime := New(newTestStore(), WithToolWorkflowQueue("service-worker"))
	request, err := runtime.prepareRegisteredRootRequest("", "registry", resolution, invocation, map[string]string{"site": "one"}, nil)
	require.NoError(t, err)
	assert.Equal(t, "service-root", request.ID)
	assert.Empty(t, request.Input.SessionID)
	assert.Empty(t, request.Input.ToolArgs)
	assert.Nil(t, request.Input.Operation)
	assert.Empty(t, runtime.agents)
	subject, ok := request.Input.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	assert.Equal(t, "records.find", subject.Tool)
	before, err := startrecipe.SnapshotRequest(request)
	require.NoError(t, err)
	invocation.Arguments[0] = 'x'
	after, err := startrecipe.SnapshotRequest(request)
	require.NoError(t, err)
	assert.Equal(t, before.Digest, after.Digest)
	_, changed := registeredRootFixture()
	changed.Arguments = []byte(`{"value":7}`)
	other, err := runtime.prepareRegisteredRootRequest("", "registry", resolution, changed, map[string]string{"site": "one"}, nil)
	require.NoError(t, err)
	snapshot, err := startrecipe.SnapshotRequest(other)
	require.NoError(t, err)
	assert.NotEqual(t, before.Digest, snapshot.Digest)
}

func TestRegisteredRootRejectsChangedSourceAndMixedInput(t *testing.T) {
	tests := []struct {
		name   string
		change func(*genregistry.ResolvedToolset, *genregistry.RegisteredInvocation)
	}{
		{"token", func(_ *genregistry.ResolvedToolset, r *genregistry.RegisteredInvocation) {
			target, _ := r.Target.Target.AsService()
			target.RegistrationToken = strings.Repeat("c", 64)
		}},
		{"schema", func(_ *genregistry.ResolvedToolset, r *genregistry.RegisteredInvocation) {
			r.Arguments = []byte(`{"value":"wrong"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolution, request := registeredRootFixture()
			test.change(resolution, request)
			runtime := New(newTestStore(), WithToolWorkflowQueue("service-worker"))
			_, err := runtime.prepareRegisteredRootRequest("", "registry", resolution, request, nil, nil)
			require.Error(t, err)
		})
	}
	resolution, invocation := registeredRootFixture()
	runtime := New(newTestStore(), WithToolWorkflowQueue("service-worker"))
	request, err := runtime.prepareRegisteredRootRequest("", "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	request.Input.ToolArgs = []byte(`{"value":2}`)
	_, err = validateRegisteredRootInput(request.Input)
	require.ErrorContains(t, err, "duplicate operation state")
}

func TestRegisteredRootDispatchesAfterDurableStart(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	store := newTestStore()
	backend := engineinmem.New()
	runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	dispatches := 0
	client := &genregistry.Client{CallResolvedToolEndpoint: func(_ context.Context, value any) (any, error) {
		dispatches++
		actual := value.(*genregistry.CallResolvedToolPayload)
		assert.Equal(t, invocation.Arguments, actual.PayloadJSON)
		assert.Equal(t, resolution.RegistrationToken, actual.ExpectedRegistrationToken)
		run, err := store.LoadRun(t.Context(), "service-root")
		require.NoError(t, err)
		assert.Equal(t, session.RunStatusRunning, run.Status)
		return nil, genregistry.MakeCallNotAdmitted(errors.New("registration replaced"))
	}}
	require.NoError(t, runtime.RegisterRegistry("registry", client, unusedRegistryPulse{}))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.Error(t, err)
	meta, err := store.LoadRun(t.Context(), "service-root")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusFailed, meta.Status)
	assert.Empty(t, meta.SessionID)
	assert.Equal(t, 1, dispatches)
	assert.Empty(t, runtime.agents)
	completion, err := backend.QueryRunCompletion(t.Context(), "service-root")
	require.NoError(t, err)
	assert.Equal(t, engine.RunStatusFailed, completion.Status)
}

type registeredRootTestEngine struct {
	engine.Engine
	execute func(context.Context, *api.ToolInput) (*api.ToolOutput, error)
}

func (backend registeredRootTestEngine) RequestCancellation(ctx context.Context, workflowID string, request engine.CancellationRequest) error {
	return backend.Engine.(engine.CancellationRequester).RequestCancellation(ctx, workflowID, request)
}

func (backend registeredRootTestEngine) RegisterExecuteToolActivity(ctx context.Context, name string, options engine.ActivityOptions, handler func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	if name == registeredRootActivity {
		handler = backend.execute
	}
	return backend.Engine.RegisterExecuteToolActivity(ctx, name, options, handler)
}

func TestRegisteredRootWorkflowCompletesAndReplaysWithoutAgent(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	store := newTestStore()
	calls := 0
	backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(_ context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
		calls++
		assert.Equal(t, *invocation.Invocation, input.Invocation)
		assert.Equal(t, invocation.Arguments, []byte(input.OriginalArguments))
		assert.Empty(t, input.SessionID)
		return &api.ToolOutput{Payload: []byte(`{"value":9007199254740993}`)}, nil
	}}
	runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	for range 2 {
		handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
		require.NoError(t, err)
		result, err := handle.Wait(t.Context())
		require.NoError(t, err)
		require.NotNil(t, result.FinalToolResult)
		assert.Equal(t, `{"value":9007199254740993}`, string(result.FinalToolResult.Result))
		assert.Nil(t, result.Final)
	}
	assert.Equal(t, 1, calls)
	meta, err := store.LoadRun(t.Context(), "service-root")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCompleted, meta.Status)
	assert.Empty(t, meta.SessionID)
	assert.Equal(t, *invocation.Invocation, meta.Origin)
	changed := *invocation
	changed.Arguments = []byte(`{"value":2}`)
	_, err = runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, &changed, nil, nil)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

func TestRegisteredRootNativeRequestUsesDeclaredWorker(t *testing.T) {
	_, invocation := registeredRootFixture()
	resolved := testNativeRegistryResolution("revision/1")
	invocation.Target.Target = genregistry.NewCatalogTargetNative(&genregistry.NativeCatalogTarget{Toolset: resolved.Toolset.Name, Tool: resolved.Toolset.Tools[0].Name})
	runtime := New(newTestStore())
	request, err := runtime.prepareRegisteredRootRequest("", "registry", resolved, invocation, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "generic.workflow", request.Workflow)
	assert.Equal(t, "generic.queue", request.TaskQueue)
	assert.Equal(t, invocation, request.Input.RegisteredInvocation)
	assert.Nil(t, request.Input.Operation)
	assert.Empty(t, request.Input.ToolArgs)
	assert.Empty(t, runtime.agents)
	_, err = startrecipe.SnapshotRequest(request)
	require.NoError(t, err)
}

func TestRegisteredRootConfirmationContinuationRecoversExactRequest(t *testing.T) {
	for _, sessionID := range []string{"session", ""} {
		t.Run("session="+sessionID, func(t *testing.T) {
			resolution, invocation := registeredRootFixture()
			resolution.Toolset.Tools[0].ConsumerContract.Confirmation = testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)).Toolset.Tools[0].ConsumerContract.Confirmation
			store := newTestStore()
			if sessionID != "" {
				_, err := store.CreateSession(t.Context(), sessionID, time.Now().UTC())
				require.NoError(t, err)
			}
			calls := 0
			backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(_ context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
				calls++
				assert.Equal(t, *invocation.Invocation, input.Invocation)
				return &api.ToolOutput{Payload: []byte(`{"value":2}`)}, nil
			}}
			runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
			require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
			request, err := runtime.prepareRegisteredRootRequest(sessionID, "registry", resolution, invocation, nil, nil)
			require.NoError(t, err)
			handle, err := runtime.startWorkflow(t.Context(), request)
			require.NoError(t, err)
			output, err := handle.Wait(t.Context())
			require.NoError(t, err)
			require.NotNil(t, output.Suspension)
			assert.Zero(t, calls)
			answer := &api.RunContinuationInput{Response: &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: output.Suspension.Pending[0].Confirmation.ID, Approved: true}}}
			next, err := runtime.prepareRegisteredContinuationRequest(t.Context(), "service-root", "successor", answer)
			require.NoError(t, err)
			before, err := startrecipe.SnapshotRequest(next)
			require.NoError(t, err)
			restarted := New(store, WithToolWorkflowQueue("replacement-worker"))
			again, err := restarted.prepareRegisteredContinuationRequest(t.Context(), "service-root", "successor", answer)
			require.NoError(t, err)
			after, err := startrecipe.SnapshotRequest(again)
			require.NoError(t, err)
			assert.Equal(t, before.Digest, after.Digest)
			changed := &api.RunContinuationInput{Response: &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: answer.Response.Confirmation.ID, Approved: false}}}
			_, err = restarted.prepareRegisteredContinuationRequest(t.Context(), "service-root", "successor", changed)
			require.ErrorIs(t, err, storage.ErrSeedConflict)
			handle, err = runtime.startWorkflow(t.Context(), next)
			require.NoError(t, err)
			output, err = handle.Wait(t.Context())
			require.NoError(t, err)
			require.NotNil(t, output.FinalToolResult)
			assert.Equal(t, 1, calls)
			predecessor, err := store.LoadRun(t.Context(), "service-root")
			require.NoError(t, err)
			assert.Equal(t, "successor", predecessor.SuccessorRunID)
		})
	}
}

// A selected tool failure is part of the durable operation result even though
// waiting for the workflow returns an error instead of a successful output.
func TestRegisteredRootFailureIsStoredBeforeWorkflowReturns(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	store := &terminalActivityFaultStore{Store: newTestStore()}
	backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(context.Context, *api.ToolInput) (*api.ToolOutput, error) {
		return &api.ToolOutput{
			Failure: &planner.ToolFailure{Kind: planner.FailureUnavailable, Error: planner.NewToolError("Selected operation unavailable"), Recovery: planner.RecoveryDirective{Action: planner.RecoveryReplan}},
			Blocks:  content.Blocks{&content.TextContent{Text: "Retained partial evidence"}},
		}, nil
	}}
	runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.ErrorContains(t, err, "Selected operation unavailable")
	owner, err := store.LoadRun(t.Context(), "service-root")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusFailed, owner.Status)
	page, err := store.ListRunRecords(t.Context(), "service-root", "", 20)
	require.NoError(t, err)
	var completed *hooks.RunCompletedEvent
	for _, record := range page.Events {
		event, err := hooks.DecodeRunlogEvent(record)
		require.NoError(t, err)
		if event, ok := event.(*hooks.RunCompletedEvent); ok {
			require.Nil(t, completed)
			completed = event
		}
	}
	require.NotNil(t, completed)
	require.Len(t, store.terminalResults, 1)
	require.NotNil(t, store.terminalResults[0])
	failure, ok := store.terminalResults[0].Outcome.AsFailure()
	require.True(t, ok)
	assert.Equal(t, string(planner.FailureUnavailable), failure.Kind)
	assert.Equal(t, "Selected operation unavailable", failure.Message)
	require.Len(t, failure.Blocks, 1)
	block, ok := failure.Blocks[0].Content.AsText()
	require.True(t, ok)
	assert.Equal(t, "Retained partial evidence", block.Text)
}

func TestRegisteredRootPreparationStopStoresExactReason(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	store := &earlyTerminalCaptureStore{Store: storageinmem.New(), stopReason: "owner-requested-stop"}
	calls := 0
	backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(context.Context, *api.ToolInput) (*api.ToolOutput, error) {
		calls++
		return &api.ToolOutput{Payload: []byte(`{"value":1}`)}, nil
	}}
	runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.Error(t, err)
	assert.Zero(t, calls)
	require.NotNil(t, store.terminal)
	assert.Equal(t, invocation.Invocation, store.terminal.Origin)
	reason, canceled := store.terminal.Outcome.AsCanceled()
	require.True(t, canceled)
	assert.Equal(t, store.stopReason, string(reason))
	_, err = store.LoadRun(t.Context(), "service-root")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func TestRegisteredRootStoppedContinuationPublishesExactRequest(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	resolution.Toolset.Tools[0].ConsumerContract.Confirmation = testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)).Toolset.Tools[0].ConsumerContract.Confirmation
	store := newTestStore()
	_, err := store.CreateSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)
	calls := 0
	backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(context.Context, *api.ToolInput) (*api.ToolOutput, error) {
		calls++
		return &api.ToolOutput{Payload: []byte(`{"value":1}`)}, nil
	}}
	runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	handle, err := runtime.StartToolOperation(t.Context(), "session", "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	_, err = store.EndSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)
	answer := &api.RunContinuationInput{Response: &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: output.Suspension.Pending[0].Confirmation.ID, Approved: true}}}
	request, err := runtime.prepareRegisteredContinuationRequest(t.Context(), "service-root", "stopped-successor", answer)
	require.NoError(t, err)
	before, err := startrecipe.SnapshotRequest(request)
	require.NoError(t, err)
	restarted := New(store, WithToolWorkflowQueue("changed-worker"))
	recovered, err := restarted.prepareRegisteredContinuationRequest(t.Context(), "service-root", "stopped-successor", answer)
	require.NoError(t, err)
	after, err := startrecipe.SnapshotRequest(recovered)
	require.NoError(t, err)
	assert.Equal(t, before.Digest, after.Digest)
	handle, err = runtime.startWorkflow(t.Context(), recovered)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.Error(t, err)
	assert.Zero(t, calls)
	successor, err := store.LoadRun(t.Context(), "stopped-successor")
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, successor.Status)
	assert.Equal(t, run.CancellationReasonSessionEnded, successor.CancellationReason)
	assert.Equal(t, *invocation.Invocation, successor.Origin)
	completion, err := backend.QueryRunCompletion(t.Context(), "stopped-successor")
	require.NoError(t, err)
	assert.Equal(t, engine.RunStatusCanceled, completion.Status)
}
