package runtime

// Tool failures constrain the work a workflow may perform. A rejected model
// response adds correction feedback without replacing those constraints.
// Only the two model-correction variants are mutually exclusive.

import (
	"errors"
	"strings"

	"goa.design/goa-ai/runtime/agent/internal/outputcontract"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	pendingModelRecovery interface {
		pendingModelRecovery()
	}

	pendingToolRecovery struct {
		outputs []*planner.ToolOutput
		catalog *RecoveryCatalog
	}

	pendingModelOutputRecovery struct {
		recovery ModelOutputRecovery
	}

	pendingModelInvocationRecovery struct {
		recovery ModelInvocationRecovery
	}
)

func (pendingModelOutputRecovery) pendingModelRecovery()     {}
func (pendingModelInvocationRecovery) pendingModelRecovery() {}

// toolRecovery returns the failed calls and catalog when the workflow is
// waiting for the planner to repair tool work.
func toolRecovery(recovery *pendingToolRecovery) ([]*planner.ToolOutput, *RecoveryCatalog) {
	if recovery == nil {
		return nil, nil
	}
	return recovery.outputs, recovery.catalog
}

// finishRecovery identifies active failures that forbid starting new operations.
// Callers pass only pending failures, never the complete historical transcript.
func finishRecovery(outputs []*planner.ToolOutput) bool {
	for _, output := range outputs {
		if output.Failure != nil && output.Failure.Recovery.Action == planner.RecoveryFinish {
			return true
		}
	}
	return false
}

// correctCallToolNames identifies the failed tools whose correction contracts
// must remain available. This is not the turn's complete advertised catalog.
// Names follow first-failure order with duplicates removed.
func correctCallToolNames(outputs []*planner.ToolOutput) []tools.Ident {
	seen := make(map[tools.Ident]struct{})
	var catalog []tools.Ident
	for _, output := range outputs {
		if output.Failure == nil || output.Failure.Recovery.Action != planner.RecoveryCorrectCall {
			continue
		}
		if _, ok := seen[output.Name]; ok {
			continue
		}
		seen[output.Name] = struct{}{}
		catalog = append(catalog, output.Name)
	}
	return catalog
}

// modelOutputRecovery returns the rejected response kind and replacement
// guidance when the workflow is waiting for corrected model output.
func modelOutputRecovery(recovery pendingModelRecovery) *ModelOutputRecovery {
	if recovery == nil {
		return nil
	}
	pending, ok := recovery.(pendingModelOutputRecovery)
	if !ok {
		return nil
	}
	result := pending.recovery
	return &result
}

// modelInvocationRecovery returns the one recorded fact when the workflow is
// waiting for a replacement under the current executable catalog. It returns
// a copy so callers cannot change workflow state after reading it.
func modelInvocationRecovery(recovery pendingModelRecovery) *ModelInvocationRecovery {
	if recovery == nil {
		return nil
	}
	pending, ok := recovery.(pendingModelInvocationRecovery)
	if !ok {
		return nil
	}
	return &pending.recovery
}

// validateModelOutputRecovery checks the activity value before a workflow
// records or reuses it.
func validateModelOutputRecovery(recovery *ModelOutputRecovery) error {
	if recovery == nil {
		return errors.New("model-output recovery is required")
	}
	if recovery.Kind != planner.ModelOutputRecoveryAnswer &&
		recovery.Kind != planner.ModelOutputRecoveryPlanning {
		return errors.New("model-output correction requires a valid recovery kind")
	}
	if strings.TrimSpace(recovery.Correction) == "" {
		return errors.New("model-output correction requires non-blank guidance")
	}
	if len(recovery.Correction) > outputcontract.MaxCorrectionBytes {
		return errors.New("model-output correction exceeds workflow boundary limit")
	}
	return nil
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
