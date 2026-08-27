package runtime

// This file represents the one pending recovery action owned by a workflow.
// Tool recovery carries failed calls and their advertised catalog. Completed
// answer recovery and pre-canonical invocation recovery carry different
// guidance because only the latter retains the normal executable tool catalog.

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

	pendingModelInvocationRecovery struct {
		correction string
	}
)

func (pendingToolRecovery) pendingPlannerRecovery()            {}
func (pendingModelOutputRecovery) pendingPlannerRecovery()     {}
func (pendingModelInvocationRecovery) pendingPlannerRecovery() {}

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

// modelInvocationCorrection returns replacement guidance when the workflow is
// waiting for a new tool call under the normal executable catalog.
func modelInvocationCorrection(recovery pendingPlannerRecovery) string {
	if recovery == nil {
		return ""
	}
	pending, ok := recovery.(pendingModelInvocationRecovery)
	if !ok {
		return ""
	}
	return pending.correction
}
