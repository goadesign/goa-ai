package temporal

// These tests give Temporal a retry policy and verify that a saved local model
// request rejection stops the activity or child workflow after one attempt.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestRequestValidationActivityIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context) error {
		calls.Add(1)
		return temporalerrors.Wrap(model.NewRequestValidationError(errors.New("invalid request")))
	}, activity.RegisterOptions{Name: "reject-request"})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: time.Second,
			RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
		})
		return temporalerrors.Wrap(workflow.ExecuteActivity(ctx, "reject-request").Get(ctx, nil))
	})
	err := env.GetWorkflowError()
	require.Error(t, err)
	assert.True(t, temporalerrors.IsRequestValidation(err))
	assert.EqualValues(t, 1, calls.Load())
	failure := hooks.RunFailureFromError(err)
	assert.Equal(t, hooks.ErrorKindModelRequest, failure.Kind)
	assert.False(t, failure.Retryable)
}

func TestRequestValidationChildIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(func(workflow.Context) error {
		calls.Add(1)
		return temporalerrors.Wrap(model.NewRequestValidationError(errors.New("invalid child request")))
	}, workflow.RegisterOptions{Name: "reject-child-request"})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
			WorkflowExecutionTimeout: time.Minute,
			RetryPolicy:              &temporal.RetryPolicy{MaximumAttempts: 3},
		})
		return temporalerrors.Wrap(workflow.ExecuteChildWorkflow(ctx, "reject-child-request").Get(ctx, nil))
	})
	err := env.GetWorkflowError()
	require.Error(t, err)
	assert.True(t, temporalerrors.IsRequestValidation(err))
	assert.EqualValues(t, 1, calls.Load())
	failure := hooks.RunFailureFromError(err)
	assert.Equal(t, hooks.ErrorKindModelRequest, failure.Kind)
	assert.False(t, failure.Retryable)
}
