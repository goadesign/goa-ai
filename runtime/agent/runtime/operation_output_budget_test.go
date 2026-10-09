// These checks keep the complete planner activity budget intact when saved
// operations have their own generated JSON encoder.
package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/mcp"
)

func TestPlanActivityOutputBudgetMeasuresExecutionContinuation(t *testing.T) {
	state := strings.Repeat("x", maxPlanActivityOutputBytes/2)
	operation, err := tooloperation.NewInput(&mcp.CallContinuation{RequestState: &state})
	require.NoError(t, err)
	for _, value := range []any{operation, *operation, (*tooloperation.Continuation)(nil)} {
		budget := &planActivityOutputBudget{}
		require.NoError(t, budget.add(value))
	}
	one := &PlanActivityOutput{Result: &PlanResult{ToolCalls: []ToolCall{{ExecutionSequence: 1, ExecutionContinuation: operation}}}}
	require.NoError(t, checkPlanActivityOutputBudget(one))
	one.Result.ToolCalls = append(one.Result.ToolCalls, one.Result.ToolCalls[0])
	require.ErrorContains(t, checkPlanActivityOutputBudget(one), "conservative encoded-size bound")
	assert.Error(t, (&planActivityOutputBudget{}).add(tooloperation.Continuation{}))
}
