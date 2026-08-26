package runtime

// This file represents the one pending recovery action owned by a workflow.
// Tool recovery carries failed calls and their advertised catalog. Model
// recovery carries guidance for replacing one rejected answer. Separate types
// prevent one planner activity from receiving both modes.

import "goa.design/goa-ai/runtime/agent/planner"

type (
	pendingPlannerRecovery interface {
		pendingPlannerRecovery()
	}

	pendingToolRecovery struct {
		outputs []*planner.ToolOutput
		catalog *RecoveryCatalog
	}

	pendingModelOutputRecovery struct {
		correction string
	}
)

func (pendingToolRecovery) pendingPlannerRecovery()        {}
func (pendingModelOutputRecovery) pendingPlannerRecovery() {}

// toolRecovery returns the failed calls and catalog when the workflow is
// waiting for the planner to repair tool work.
func toolRecovery(recovery pendingPlannerRecovery) ([]*planner.ToolOutput, *RecoveryCatalog) {
	if recovery == nil {
		return nil, nil
	}
	pending, ok := recovery.(pendingToolRecovery)
	if !ok {
		return nil, nil
	}
	return pending.outputs, pending.catalog
}

// modelOutputCorrection returns replacement guidance when the workflow is
// waiting for a new final answer.
func modelOutputCorrection(recovery pendingPlannerRecovery) string {
	if recovery == nil {
		return ""
	}
	pending, ok := recovery.(pendingModelOutputRecovery)
	if !ok {
		return ""
	}
	return pending.correction
}
