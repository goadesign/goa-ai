package temporal

// Old workflow starts without engine proof are rejected before activity replay.
// Successful current-history tests must not imply those histories were upgraded.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
)

func TestProductionWorkflowRejectsHistoryWithoutAcceptedRequest(t *testing.T) {
	for _, test := range []struct{ name, seed, rejection string }{
		{"old input without publication", "", "published initial history is required"},
		{"current input without memo", "published", "accepted request"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plannerStub, handler := productionReplayWorkflow(t)
			// Construct each rejected shape independently. Neither case upgrades
			// saved history or supplies a fabricated engine acceptance memo.
			input, err := NewAgentDataConverter().ToPayloads(&api.RunInput{
				AgentID: productionReplayAgentID, RunID: productionReplayRunID,
				SessionID: productionReplaySessionID, TurnID: productionReplayTurnID, SeedEndID: test.seed,
			})
			require.NoError(t, err)
			started := workflowExecutionStartedEvent(1, productionReplayWorkflowName, productionReplayTaskQueue, input)
			history := deserializeReplayHistory(t, syntheticProductionHistory(t, &api.PlanActivityOutput{}, false, started))
			var returned error
			replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{DataConverter: NewAgentDataConverter()})
			require.NoError(t, err)
			replayer.RegisterWorkflowWithOptions(func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
				out, err := handler(ctx, input)
				returned = err
				return out, err
			}, workflow.RegisterOptions{Name: productionReplayWorkflowName})
			require.Error(t, replayer.ReplayWorkflowHistory(nil, history))
			require.ErrorContains(t, returned, test.rejection)
			assert.Zero(t, plannerStub.calls.Load())
			assert.Nil(t, history.Events[0].GetWorkflowExecutionStartedEventAttributes().Memo)
		})
	}
}
