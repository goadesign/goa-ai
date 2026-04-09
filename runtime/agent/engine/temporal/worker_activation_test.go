package temporal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

// These tests lock the Temporal adapter's activation contract around startup
// retries, sealed registration, and fatal worker handling.

func TestSealRegistrationStartsQueuedWorkers(t *testing.T) {
	t.Parallel()

	fake := &fakeWorker{}
	eng := newActivationTestEngine(t, fake)
	err := eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		Queue:               "queue.alpha",
		StartToCloseTimeout: time.Minute,
	}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	})
	require.NoError(t, err)

	bundle := eng.workers["queue.alpha"]
	require.NotNil(t, bundle)
	require.False(t, bundle.isStarted())

	require.NoError(t, eng.SealRegistration(context.Background()))
	require.True(t, bundle.isStarted())
	assert.Equal(t, 1, fake.startCallCount())

	require.NoError(t, eng.SealRegistration(context.Background()))
	assert.Equal(t, 1, fake.startCallCount())
}

func TestSealRegistrationRetriesWorkerActivation(t *testing.T) {
	t.Parallel()

	fake := &fakeWorker{startErrors: []error{errors.New("frontend not healthy"), nil}}
	eng := newActivationTestEngine(t, fake)
	err := eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		Queue:               "queue.alpha",
		StartToCloseTimeout: time.Minute,
	}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	require.NoError(t, eng.SealRegistration(ctx))
	assert.Equal(t, 2, fake.startCallCount())
	assert.True(t, eng.workers["queue.alpha"].isStarted())
}

func TestSealRegistrationTimesOutWhenActivationNeverSucceeds(t *testing.T) {
	t.Parallel()

	fake := &fakeWorker{startErrors: []error{errors.New("frontend not healthy")}}
	eng := newActivationTestEngine(t, fake)
	err := eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		Queue:               "queue.alpha",
		StartToCloseTimeout: time.Minute,
	}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err = eng.SealRegistration(ctx)
	require.ErrorContains(t, err, `temporal worker "queue.alpha" activation did not complete`)
	assert.False(t, eng.workers["queue.alpha"].isStarted())
	assert.GreaterOrEqual(t, fake.startCallCount(), 1)
}

func TestSealRegistrationCanRetryAfterActivationTimeout(t *testing.T) {
	t.Parallel()

	fake := &fakeWorker{startErrors: []error{errors.New("frontend not healthy"), nil}}
	eng := newActivationTestEngine(t, fake)
	eng.activationRetryInterval = 50 * time.Millisecond
	err := eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		Queue:               "queue.alpha",
		StartToCloseTimeout: time.Minute,
	}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	})
	require.NoError(t, err)

	firstCtx, firstCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer firstCancel()
	err = eng.SealRegistration(firstCtx)
	require.ErrorContains(t, err, `temporal worker "queue.alpha" activation did not complete`)
	require.False(t, eng.workers["queue.alpha"].isStarted())

	secondCtx, secondCancel := context.WithTimeout(context.Background(), time.Second)
	defer secondCancel()
	require.NoError(t, eng.SealRegistration(secondCtx))
	require.True(t, eng.workers["queue.alpha"].isStarted())
	assert.Equal(t, 2, fake.startCallCount())
}

func TestRegisterWorkflowRejectsRegistrationAfterSeal(t *testing.T) {
	t.Parallel()

	fake := &fakeWorker{}
	eng := newActivationTestEngine(t, fake)
	err := eng.RegisterWorkflow(context.Background(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(ctx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			return &api.RunOutput{}, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, eng.SealRegistration(context.Background()))

	err = eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		StartToCloseTimeout: time.Minute,
	}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	})
	require.ErrorContains(t, err, "registration is sealed")
}

func TestWorkerFatalErrorHandlerRunsAndCloseDoesNotStopTwice(t *testing.T) {
	t.Parallel()

	fake := &fakeWorker{}
	eng := newActivationTestEngine(t, fake)

	fatalErrc := make(chan error, 1)
	eng.workerOpts.OnFatalError = func(err error) {
		fatalErrc <- err
	}
	err := eng.RegisterPlannerActivity(context.Background(), "planner.activity", engine.ActivityOptions{
		Queue:               "queue.alpha",
		StartToCloseTimeout: time.Minute,
	}, func(ctx context.Context, input *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return &api.PlanActivityOutput{}, nil
	})
	require.NoError(t, err)
	require.NoError(t, eng.SealRegistration(context.Background()))

	bundle := eng.workers["queue.alpha"]
	require.True(t, bundle.isStarted())

	fake.failFatally(errors.New("worker lost matching connection"))

	select {
	case err := <-fatalErrc:
		require.ErrorContains(t, err, `temporal worker "queue.alpha" reported fatal error`)
	case <-time.After(time.Second):
		t.Fatal("expected fatal worker callback")
	}
	assert.False(t, bundle.isStarted())

	require.NoError(t, eng.Close())
	assert.Equal(t, 1, fake.stopCallCount())
}

func newActivationTestEngine(t *testing.T, fake *fakeWorker) *Engine {
	t.Helper()

	eng := newTestEngine(t)
	eng.activationRetryInterval = time.Millisecond
	eng.workerFactory = func(_ client.Client, _ string, opts worker.Options) worker.Worker {
		fake.captureOptions(opts)
		return fake
	}
	return eng
}

type fakeWorker struct {
	mu sync.Mutex

	startErrors []error
	startCalls  int
	stopCalls   int

	onFatalError func(error)
}

func (w *fakeWorker) Start() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	call := w.startCalls
	w.startCalls++
	if len(w.startErrors) == 0 {
		return nil
	}
	if call >= len(w.startErrors) {
		return w.startErrors[len(w.startErrors)-1]
	}
	return w.startErrors[call]
}

func (w *fakeWorker) Run(<-chan interface{}) error {
	panic("fakeWorker.Run should not be called")
}

func (w *fakeWorker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopCalls++
}

func (w *fakeWorker) RegisterWorkflow(any) {}

func (w *fakeWorker) RegisterWorkflowWithOptions(any, workflow.RegisterOptions) {}

func (w *fakeWorker) RegisterDynamicWorkflow(any, workflow.DynamicRegisterOptions) {}

func (w *fakeWorker) RegisterActivity(any) {}

func (w *fakeWorker) RegisterActivityWithOptions(any, activity.RegisterOptions) {}

func (w *fakeWorker) RegisterDynamicActivity(any, activity.DynamicRegisterOptions) {}

func (w *fakeWorker) RegisterNexusService(*nexus.Service) {}

func (w *fakeWorker) captureOptions(opts worker.Options) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onFatalError = opts.OnFatalError
}

func (w *fakeWorker) failFatally(err error) {
	w.mu.Lock()
	callback := w.onFatalError
	w.mu.Unlock()

	if callback != nil {
		callback(err)
	}
	w.Stop()
}

func (w *fakeWorker) startCallCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startCalls
}

func (w *fakeWorker) stopCallCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopCalls
}
