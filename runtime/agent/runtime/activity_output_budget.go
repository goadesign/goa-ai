package runtime

// Planner activities use the engine codec's conservative size walk
// before returning output. This file preserves the runtime's rejection type so
// oversized planner output continues through its existing failure handling.

import (
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

type (
	planActivityOutputBudgetError struct {
		reason string
	}

	planActivityOutputBudget struct {
		budget workflowcodec.Budget
	}
)

const (
	maxPlanActivityOutputBytes  = engine.MaxPayloadBytes
	maxPlanActivityOutputVisits = 100_000
)

// Error states the activity-envelope contract that rejected the output.
func (e *planActivityOutputBudgetError) Error() string { return e.reason }

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
