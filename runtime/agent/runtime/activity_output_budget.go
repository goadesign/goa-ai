// Package runtime checks planner activities with the engine codec's conservative size check before
// returning output. Provider failures and immutable MCP operations share one
// budget; oversized output retains the runtime's existing rejection type.
package runtime

import (
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

type (
	// planActivityOutputBudgetError reports an output rejected before encoding.
	planActivityOutputBudgetError struct {
		reason string
	}

	// planActivityOutputBudget retains one size total across planner output and
	// its later events, so they cannot each spend the full activity allowance.
	planActivityOutputBudget struct {
		budget workflowcodec.Budget
	}
)

const (
	maxPlanActivityOutputBytes  = engine.MaxPayloadBytes
	maxPlanActivityOutputVisits = 100_000
)

// Error states the activity-envelope contract that rejected the output.
func (e *planActivityOutputBudgetError) Error() string {
	return e.reason
}

// checkPlanActivityOutputBudget checks the complete output before encoding it.
func checkPlanActivityOutputBudget(output *PlanActivityOutput) error {
	return (&planActivityOutputBudget{}).add(output)
}

// add includes one output branch in the shared byte and traversal budget.
func (b *planActivityOutputBudget) add(value any) error {
	if err := b.budget.AddEncodedSource(value); err != nil {
		return &planActivityOutputBudgetError{reason: err.Error()}
	}
	return nil
}
