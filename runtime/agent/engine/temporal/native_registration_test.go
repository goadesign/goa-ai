package temporal

// These tests register SDK handlers without connecting to a server. Successful
// registrations determine queue inventory; failed attempts never advertise work.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	nativeActivityGroup struct{}

	nativeHandlerCapture struct {
		*fakeWorker
		handler       any
		options       workflow.RegisterOptions
		workerOptions worker.Options
	}
)

func TestNativeRegistrationReportsActualTaskQueues(t *testing.T) {
	eng := newTestEngine(t)
	t.Cleanup(func() { require.NoError(t, eng.Close()) })
	workflowHandler := func(workflow.Context) error { return nil }
	activityHandler := func(context.Context) (bool, error) { return true, nil }
	for _, queue := range []string{"workflow.only", "shared"} {
		require.NoError(t, eng.RegisterNativeWorkflow(t.Context(), queue, workflowHandler, workflow.RegisterOptions{Name: "native.workflow"}))
	}
	for _, queue := range []string{"activity.only", "shared"} {
		require.NoError(t, eng.RegisterNativeActivity(t.Context(), queue, activityHandler, activity.RegisterOptions{Name: "native.activity"}))
	}
	require.NoError(t, eng.RegisterNativeWorkflow(t.Context(), "", workflowHandler, workflow.RegisterOptions{Name: "default.workflow"}))
	require.NoError(t, eng.RegisterPlannerActivity(t.Context(), "typed.activity", engine.ActivityOptions{StartToCloseTimeout: time.Minute}, func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	}))
	want := []RegisteredTaskQueue{
		{Name: "activity.only", Kinds: []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_ACTIVITY}},
		{Name: "default.queue", Kinds: []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY}},
		{Name: "shared", Kinds: []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY}},
		{Name: "workflow.only", Kinds: []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW}},
	}
	got := eng.RegisteredTaskQueues()
	assert.Equal(t, want, got)
	got[0].Name, got[1].Kinds[0] = "changed", enumspb.TASK_QUEUE_TYPE_UNSPECIFIED
	assert.Equal(t, want, eng.RegisteredTaskQueues())
	require.ErrorContains(t, eng.RegisterNativeWorkflow(t.Context(), "shared", workflowHandler, workflow.RegisterOptions{Name: "native.workflow"}), "already registered")
	require.ErrorContains(t, eng.RegisterNativeActivity(t.Context(), "shared", activityHandler, activity.RegisterOptions{Name: "native.activity"}), "already registered")
	assert.Equal(t, want, eng.RegisteredTaskQueues())
}

func TestNativeAndTypedNamesCannotCollide(t *testing.T) {
	for _, nativeFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "typed first", true: "native first"}[nativeFirst], func(t *testing.T) {
			eng := newTestEngine(t)
			t.Cleanup(func() { require.NoError(t, eng.Close()) })
			typedWorkflow := func() error {
				return eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{Name: "workflow", TaskQueue: "typed", Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) { return nil, nil }})
			}
			nativeWorkflow := func() error {
				return eng.RegisterNativeWorkflow(t.Context(), "native", func(workflow.Context) error { return nil }, workflow.RegisterOptions{Name: "workflow"})
			}
			typedActivity := func() error {
				return eng.RegisterPlannerActivity(t.Context(), "activity", engine.ActivityOptions{Queue: "typed", StartToCloseTimeout: time.Minute}, func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) { return nil, nil })
			}
			nativeActivity := func() error {
				return eng.RegisterNativeActivity(t.Context(), "native", func(context.Context) error { return nil }, activity.RegisterOptions{Name: "activity"})
			}
			firstWorkflow, secondWorkflow := typedWorkflow, nativeWorkflow
			firstActivity, secondActivity := typedActivity, nativeActivity
			if nativeFirst {
				firstWorkflow, secondWorkflow = nativeWorkflow, typedWorkflow
				firstActivity, secondActivity = nativeActivity, typedActivity
			}
			require.NoError(t, firstWorkflow())
			require.NoError(t, firstActivity())
			require.ErrorContains(t, secondWorkflow(), "already registered")
			require.ErrorContains(t, secondActivity(), "already registered")
			assert.Len(t, eng.RegisteredTaskQueues(), 1)
			assert.Len(t, eng.workers, 1)
		})
	}
}

func TestNativeRegistrationKeepsLifecycleRules(t *testing.T) {
	for _, kind := range []string{"workflow", "activity"} {
		t.Run(kind, func(t *testing.T) {
			register := func(ctx context.Context, eng *Engine, handler any, name string) error {
				if kind == "workflow" {
					return eng.RegisterNativeWorkflow(ctx, "queue", handler, workflow.RegisterOptions{Name: name})
				}
				return eng.RegisterNativeActivity(ctx, "queue", handler, activity.RegisterOptions{Name: name})
			}
			var handler any = func(context.Context) error { return nil }
			if kind == "workflow" {
				handler = func(workflow.Context) error { return nil }
			}
			eng := newActivationTestEngine(t, &fakeWorker{})
			t.Cleanup(func() { require.NoError(t, eng.Close()) })
			require.ErrorContains(t, register(t.Context(), eng, handler, ""), "name cannot be empty")
			canceled, cancel := context.WithCancel(t.Context())
			cancel()
			require.ErrorIs(t, register(canceled, eng, handler, "canceled"), context.Canceled)
			assert.Empty(t, eng.RegisteredTaskQueues())
			require.NoError(t, register(t.Context(), eng, handler, "accepted"))
			require.NoError(t, eng.SealRegistration(t.Context()))
			require.ErrorContains(t, register(t.Context(), eng, handler, "late"), "registration is sealed")
			assert.Len(t, eng.RegisteredTaskQueues(), 1)
			caller, err := NewClient(Options{ClientOptions: &client.Options{}})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, caller.Close()) })
			require.ErrorContains(t, register(t.Context(), caller, handler, "client"), "client mode")
			assert.Empty(t, caller.RegisteredTaskQueues())
		})
	}
}

func TestNativeRegistrationRejectsStructGroupsBeforeMutation(t *testing.T) {
	eng := newTestEngine(t)
	t.Cleanup(func() { require.NoError(t, eng.Close()) })
	for _, handler := range []any{&nativeActivityGroup{}, 42, nil} {
		require.ErrorContains(t, eng.RegisterNativeActivity(t.Context(), "rejected", handler, activity.RegisterOptions{Name: "prefix"}), "one function handler")
		require.ErrorContains(t, eng.RegisterNativeWorkflow(t.Context(), "rejected", handler, workflow.RegisterOptions{Name: "workflow"}), "one function handler")
	}
	require.Empty(t, eng.workers, "rejected groups must never create an SDK worker or install handlers")
	require.Empty(t, eng.activityOptions)
	require.Empty(t, eng.workflows)
	require.Empty(t, eng.pendingActivities)
	require.Empty(t, eng.pendingWorkflows)
	require.Empty(t, eng.RegisteredTaskQueues())
	require.NoError(t, eng.RegisterNativeActivity(t.Context(), "accepted", func(context.Context) error { return nil }, activity.RegisterOptions{Name: "prefix"}))
	require.Equal(t, []RegisteredTaskQueue{{Name: "accepted", Kinds: []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_ACTIVITY}}}, eng.RegisteredTaskQueues())
}

func TestNativeSDKValidationReleasesFailedReservations(t *testing.T) {
	eng := newTestEngine(t)
	t.Cleanup(func() { require.NoError(t, eng.Close()) })
	assert.Panics(t, func() {
		require.NoError(t, eng.RegisterNativeWorkflow(t.Context(), "failed", func(context.Context) error { return nil }, workflow.RegisterOptions{Name: "workflow"}))
	})
	assert.Panics(t, func() {
		require.NoError(t, eng.RegisterNativeActivity(t.Context(), "failed", func() {}, activity.RegisterOptions{Name: "activity"}))
	})
	assert.Empty(t, eng.RegisteredTaskQueues())
	assert.Empty(t, eng.pendingWorkflows)
	assert.Empty(t, eng.pendingActivities)
	require.NoError(t, eng.RegisterNativeWorkflow(t.Context(), "accepted", func(workflow.Context) error { return nil }, workflow.RegisterOptions{Name: "workflow"}))
	require.NoError(t, eng.RegisterNativeActivity(t.Context(), "accepted", func(context.Context) error { return nil }, activity.RegisterOptions{Name: "activity"}))
	assert.Equal(t, []RegisteredTaskQueue{{Name: "accepted", Kinds: []enumspb.TaskQueueType{enumspb.TASK_QUEUE_TYPE_WORKFLOW, enumspb.TASK_QUEUE_TYPE_ACTIVITY}}}, eng.RegisteredTaskQueues())
}

func TestNativeWorkflowAcceptsSDKInputWithoutAgentStartRecipe(t *testing.T) {
	eng := newTestEngine(t)
	t.Cleanup(func() { require.NoError(t, eng.Close()) })
	capture := &nativeHandlerCapture{fakeWorker: &fakeWorker{}}
	eng.workerFactory = func(_ client.Client, _ string, options worker.Options) worker.Worker {
		capture.workerOptions = options
		return capture
	}
	called := false
	require.NoError(t, eng.RegisterNativeWorkflow(t.Context(), "queue", func(workflow.Context) error {
		called = true
		return nil
	}, workflow.RegisterOptions{Name: "native"}))
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.SetWorkerOptions(capture.workerOptions)
	environment.RegisterWorkflowWithOptions(capture.handler, capture.options)
	environment.ExecuteWorkflow("native")
	require.NoError(t, environment.GetWorkflowError())
	assert.True(t, called)
}

func (w *nativeHandlerCapture) RegisterWorkflowWithOptions(handler any, options workflow.RegisterOptions) {
	w.handler, w.options = handler, options
}

// Observe is a valid SDK activity method, so rejecting its group tests the
// adapter's single-function contract rather than an invalid SDK signature.
func (*nativeActivityGroup) Observe(context.Context) error {
	return nil
}
