package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	"go.temporal.io/sdk/activity"
	temporalsdk "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestApplyActivityDefaultsUsesTemporalPlannerDefaults(t *testing.T) {
	t.Parallel()

	eng := &Engine{
		activityDefaults: ActivityDefaults{
			Planner: ActivityTimeoutDefaults{
				QueueWaitTimeout: 12 * time.Second,
				LivenessTimeout:  4 * time.Second,
			},
		},
	}

	opts := eng.applyActivityClassDefaults(activityKindPlanner, engine.ActivityOptions{
		StartToCloseTimeout: time.Minute,
	})

	require.Equal(t, time.Minute, opts.StartToCloseTimeout)
	require.Equal(t, 12*time.Second, opts.ScheduleToStartTimeout)
	require.Equal(t, 4*time.Second, opts.HeartbeatTimeout)
}

func TestDerivedWorkflowContextsPreserveWorkflowTime(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		wfCtx := &temporalWorkflowContext{
			engine: &Engine{},
			ctx:    ctx,
		}
		now := wfCtx.Now()
		if !wfCtx.Detached().Now().Equal(now) {
			return errors.New("detached context changed workflow time")
		}
		child, cancel := wfCtx.WithCancel()
		defer cancel()
		if !child.Now().Equal(now) {
			return errors.New("cancelable context changed workflow time")
		}
		return nil
	})

	require.NoError(t, env.GetWorkflowError())
}

func TestActivityOptionsForUsesExplicitTimeoutFields(t *testing.T) {
	t.Parallel()

	wf := &temporalWorkflowContext{
		engine: &Engine{
			defaultQueue: "default.queue",
			activityOptions: map[string]engine.ActivityOptions{
				"planner": {
					Queue:                  "planner.queue",
					ScheduleToStartTimeout: 12 * time.Second,
					ScheduleToCloseTimeout: 2 * time.Minute,
					StartToCloseTimeout:    time.Minute,
					HeartbeatTimeout:       4 * time.Second,
					RetryPolicy: engine.RetryPolicy{
						MaxAttempts:        3,
						InitialInterval:    time.Second,
						BackoffCoefficient: 2,
					},
				},
			},
		},
	}

	opts := wf.activityOptionsFor("planner", engine.ActivityOptions{
		Queue:                  "override.queue",
		ScheduleToCloseTimeout: 75 * time.Second,
		StartToCloseTimeout:    90 * time.Second,
		HeartbeatTimeout:       7 * time.Second,
	})

	require.Equal(t, "override.queue", opts.TaskQueue)
	require.Equal(t, 12*time.Second, opts.ScheduleToStartTimeout)
	require.Equal(t, 75*time.Second, opts.ScheduleToCloseTimeout)
	require.Equal(t, 90*time.Second, opts.StartToCloseTimeout)
	require.Equal(t, 7*time.Second, opts.HeartbeatTimeout)
	require.NotNil(t, opts.RetryPolicy)
	require.EqualValues(t, 3, opts.RetryPolicy.MaximumAttempts)
	require.Equal(t, time.Second, opts.RetryPolicy.InitialInterval)
	require.InDelta(t, 2.0, opts.RetryPolicy.BackoffCoefficient, 0.000001)
}

func TestActivityOptionsForLeavesQueueWaitUnsetWithoutTemporalDefault(t *testing.T) {
	t.Parallel()

	wf := &temporalWorkflowContext{
		engine: &Engine{
			defaultQueue:    "default.queue",
			activityOptions: make(map[string]engine.ActivityOptions),
		},
	}

	opts := wf.activityOptionsFor("tool", engine.ActivityOptions{
		StartToCloseTimeout: 45 * time.Second,
	})

	require.Equal(t, "default.queue", opts.TaskQueue)
	require.Equal(t, 45*time.Second, opts.StartToCloseTimeout)
	require.Zero(t, opts.ScheduleToStartTimeout)
	require.Zero(t, opts.HeartbeatTimeout)
}

func TestActivityOptionsForCapsHeartbeatToAttemptBudget(t *testing.T) {
	t.Parallel()

	wf := &temporalWorkflowContext{
		engine: &Engine{
			defaultQueue: "default.queue",
			activityOptions: map[string]engine.ActivityOptions{
				"planner": {
					StartToCloseTimeout: time.Minute,
					HeartbeatTimeout:    20 * time.Second,
				},
			},
		},
	}

	opts := wf.activityOptionsFor("planner", engine.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
	})

	require.Equal(t, 5*time.Second, opts.StartToCloseTimeout)
	require.Equal(t, 5*time.Second, opts.HeartbeatTimeout)
}

func TestExecutePlannerActivityBoundsRetriesByScheduleToClose(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	attempts := 0
	var activityErr error
	env.RegisterActivityWithOptions(
		func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
			attempts++
			return nil, errors.New("retry planner")
		},
		activity.RegisterOptions{Name: "planner"},
	)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		wfCtx := &temporalWorkflowContext{
			engine: &Engine{},
			ctx:    ctx,
		}
		_, activityErr = wfCtx.ExecutePlannerActivity(engine.PlannerActivityCall{
			Name:  "planner",
			Input: &api.PlanActivityInput{},
			Options: engine.ActivityOptions{
				ScheduleToCloseTimeout: 5 * time.Second,
				StartToCloseTimeout:    time.Minute,
				RetryPolicy: engine.RetryPolicy{
					MaxAttempts:        100,
					InitialInterval:    time.Second,
					BackoffCoefficient: 2,
				},
			},
		})
		return nil
	})

	require.NoError(t, env.GetWorkflowError())
	require.Error(t, activityErr)
	require.Greater(t, attempts, 1)
	require.Less(t, attempts, 100)
}

// TestNormalizeTemporalPlannerError builds the failure envelope emitted by
// Temporal Server. The SDK test environment returns the last application
// failure when retry backoff exhausts Schedule-to-Close, so it cannot exercise
// this server boundary directly.
func TestNormalizeTemporalPlannerError(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantDeadline bool
	}{
		{
			name: "schedule to close owns total deadline",
			err: temporalActivityTimeoutError(
				enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE,
				enumspb.RETRY_STATE_TIMEOUT,
			),
			wantDeadline: true,
		},
		{
			name: "start to close remains attempt timeout",
			err: temporalActivityTimeoutError(
				enumspb.TIMEOUT_TYPE_START_TO_CLOSE,
				enumspb.RETRY_STATE_TIMEOUT,
			),
		},
		{
			name: "schedule to start remains queue timeout",
			err: temporalActivityTimeoutError(
				enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START,
				enumspb.RETRY_STATE_TIMEOUT,
			),
		},
		{
			name: "heartbeat remains liveness timeout",
			err: temporalActivityTimeoutError(
				enumspb.TIMEOUT_TYPE_HEARTBEAT,
				enumspb.RETRY_STATE_TIMEOUT,
			),
		},
		{
			name: "planner returned timeout remains activity failure",
			err: temporalActivityTimeoutError(
				enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE,
				enumspb.RETRY_STATE_UNSPECIFIED,
			),
		},
		{
			name: "nested timeout is not the activity deadline",
			err:  temporalActivityNestedTimeoutError(),
		},
		{
			name: "provider timeout remains activity failure",
			err:  context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := normalizeTemporalPlannerError(test.err)
			if test.wantDeadline {
				require.ErrorIs(t, err, engine.ErrPlannerActivityDeadlineExceeded)
				return
			}
			require.NotErrorIs(t, err, engine.ErrPlannerActivityDeadlineExceeded)
		})
	}
}

func TestTemporalReceiverReceiveReturnsCanceledBeforeLaterSignal(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	const signalName = "receiver.signal"

	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(signalName, "late-value")
	}, 2*time.Second)

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		recv := &temporalReceiver[string]{
			ctx: ctx,
			ch:  workflow.GetSignalChannel(ctx, signalName),
		}
		_, err := recv.Receive(context.Background())
		return err
	})

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.ErrorContains(t, err, "canceled")
}

func TestTemporalReceiverReceiveWithTimeoutReturnsCanceledBeforeLaterSignal(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	const signalName = "receiver.timeout.signal"

	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(signalName, "late-value")
	}, 2*time.Second)

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		recv := &temporalReceiver[string]{
			ctx: ctx,
			ch:  workflow.GetSignalChannel(ctx, signalName),
		}
		_, err := recv.ReceiveWithTimeout(context.Background(), 10*time.Second)
		return err
	})

	err := env.GetWorkflowError()
	require.Error(t, err)
	require.ErrorContains(t, err, "canceled")
}

func temporalActivityTimeoutError(timeoutType enumspb.TimeoutType, retryState enumspb.RetryState) error {
	return temporalsdk.GetDefaultFailureConverter().FailureToError(&failurepb.Failure{
		Message: "planner activity failed",
		Cause: &failurepb.Failure{
			Message: "planner activity timeout",
			FailureInfo: &failurepb.Failure_TimeoutFailureInfo{
				TimeoutFailureInfo: &failurepb.TimeoutFailureInfo{
					TimeoutType: timeoutType,
				},
			},
		},
		FailureInfo: &failurepb.Failure_ActivityFailureInfo{
			ActivityFailureInfo: &failurepb.ActivityFailureInfo{
				RetryState: retryState,
			},
		},
	})
}

func temporalActivityNestedTimeoutError() error {
	return temporalsdk.GetDefaultFailureConverter().FailureToError(&failurepb.Failure{
		Message: "planner activity failed",
		Cause: &failurepb.Failure{
			Message: "planner failure",
			Cause: &failurepb.Failure{
				Message: "nested timeout",
				FailureInfo: &failurepb.Failure_TimeoutFailureInfo{
					TimeoutFailureInfo: &failurepb.TimeoutFailureInfo{
						TimeoutType: enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE,
					},
				},
			},
			FailureInfo: &failurepb.Failure_ApplicationFailureInfo{
				ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
					Type: "planner_failure",
				},
			},
		},
		FailureInfo: &failurepb.Failure_ActivityFailureInfo{
			ActivityFailureInfo: &failurepb.ActivityFailureInfo{
				RetryState: enumspb.RETRY_STATE_TIMEOUT,
			},
		},
	})
}
