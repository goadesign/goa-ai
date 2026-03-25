package temporal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
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

func TestActivityOptionsForUsesExplicitTimeoutFields(t *testing.T) {
	t.Parallel()

	wf := &temporalWorkflowContext{
		engine: &Engine{
			defaultQueue: "default.queue",
			activityOptions: map[string]engine.ActivityOptions{
				"planner": {
					Queue:                  "planner.queue",
					ScheduleToStartTimeout: 12 * time.Second,
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
		Queue:               "override.queue",
		StartToCloseTimeout: 90 * time.Second,
		HeartbeatTimeout:    7 * time.Second,
	})

	require.Equal(t, "override.queue", opts.TaskQueue)
	require.Equal(t, 12*time.Second, opts.ScheduleToStartTimeout)
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
