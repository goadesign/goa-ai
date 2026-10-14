package temporal

// The workflow context reads Temporal's accepted policy, including its explicit
// unlimited form; attempt numbers and caller input cannot replace that policy.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

func TestRequireSingleAttemptUsesAcceptedPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy *temporal.RetryPolicy
		reject bool
	}{
		{name: "absent"},
		{name: "single", policy: &temporal.RetryPolicy{MaximumAttempts: 1}},
		{name: "multiple", policy: &temporal.RetryPolicy{MaximumAttempts: 2}, reject: true},
		{name: "unlimited", policy: &temporal.RetryPolicy{}, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				// The SDK test environment omits RetryPolicy from start options; supply
				// the observed execution information that the real worker receives.
				workflow.GetInfo(ctx).RetryPolicy = test.policy
				w := newTemporalWorkflowContext(&Engine{}, ctx)
				canceled, cancel := w.WithCancel()
				cancel()
				for _, derived := range []engine.WorkflowContext{w, canceled, canceled.Detached(), w.Detached()} {
					err := derived.RequireSingleAttempt()
					if test.reject {
						assert.True(t, temporalerrors.IsRequestValidation(err))
					} else {
						assert.NoError(t, err)
					}
				}
				return nil
			})
			require.NoError(t, env.GetWorkflowError())
		})
	}
}
