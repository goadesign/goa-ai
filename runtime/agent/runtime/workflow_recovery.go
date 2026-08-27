package runtime

// This file represents the one pending recovery action owned by a workflow.
// Tool recovery carries failed calls and their advertised catalog. Completed
// answer recovery carries replacement guidance. Pre-canonical invocation
// recovery carries exactly one generated correction or rejected tool name and
// retains the normal executable tool catalog.

import (
	"errors"
	"strings"

	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/planner"
)

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
		recovery ModelInvocationRecovery
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

// modelInvocationRecovery returns the one recorded fact when the workflow is
// waiting for a new tool call under the normal executable catalog. It returns
// a copy so callers cannot change workflow state after reading it.
func modelInvocationRecovery(recovery pendingPlannerRecovery) *ModelInvocationRecovery {
	if recovery == nil {
		return nil
	}
	pending, ok := recovery.(pendingModelInvocationRecovery)
	if !ok {
		return nil
	}
	return &pending.recovery
}

// validateModelInvocationRecovery checks the activity value before a workflow
// records or reuses it. Exactly one variant must be present; generated
// correction guidance also retains its existing non-blank and size limits.
func validateModelInvocationRecovery(recovery *ModelInvocationRecovery) error {
	if recovery == nil {
		return errors.New("model-invocation recovery is required")
	}
	correctionPresent := recovery.Correction != ""
	namePresent := recovery.UnadvertisedToolName != ""
	if correctionPresent == namePresent {
		return errors.New("model-invocation recovery requires exactly one recovery variant")
	}
	if !correctionPresent {
		return nil
	}
	if strings.TrimSpace(recovery.Correction) == "" {
		return errors.New("model-invocation correction requires non-blank guidance")
	}
	if len(recovery.Correction) > outputcontract.MaxCorrectionBytes {
		return errors.New("model-invocation correction exceeds workflow boundary limit")
	}
	return nil
}
