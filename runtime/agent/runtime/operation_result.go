// Package runtime uses the selected operation's actual output to produce
// durable result data that callers can read. The terminal writer does not use
// observer text or select a tool event from execution history.
package runtime

import (
	"slices"

	genrunoutput "goa.design/goa-ai/registry/gen/runoutput"
	gentoolbounds "goa.design/goa-ai/registry/gen/toolbounds"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
)

// operationResultFromRunOutput returns the selected operation's exact result.
// No tool result means no operation result; a terminal writer separately checks
// that a successfully completed named operation supplied its required outcome.
func operationResultFromRunOutput(output *api.RunOutput) (*genrunoutput.OperationResult, error) {
	if output == nil || output.FinalToolResult == nil {
		return nil, nil
	}
	return operationResultFromToolEvent(output.FinalToolResult)
}

// operationResultFromToolEvent copies the accepted selected result before later
// reporting or cleanup can fail. The copy retains its exact value and content.
func operationResultFromToolEvent(original *api.ToolEvent) (*genrunoutput.OperationResult, error) {
	blocks, err := publicContentBlocks(original.Blocks)
	if err != nil {
		return nil, err
	}
	result := &genrunoutput.OperationResult{}
	if original.Failure != nil {
		if err := planner.ValidateToolFailure(original.Failure); err != nil {
			return nil, err
		}
		result.Outcome = genrunoutput.NewOperationOutcomeFailure(&genrunoutput.OperationFailure{Kind: string(original.Failure.Kind), Message: original.Failure.Error.Message, Blocks: blocks})
		return result, nil
	}
	success := &genrunoutput.OperationSuccess{Blocks: blocks}
	if len(original.Result) == 0 {
		success.Value = genrunoutput.NewOperationValueEmpty(&genrunoutput.EmptyOperationResult{})
	} else {
		success.Value = genrunoutput.NewOperationValueJSON(genrunoutput.OperationValueBranchJSON(slices.Clone(original.Result)))
	}
	if original.Bounds != nil {
		success.Bounds = &gentoolbounds.ResultBounds{}
		success.Bounds.CreateFromBounds(agent.CloneBounds(original.Bounds))
	}
	result.Outcome = genrunoutput.NewOperationOutcomeSuccess(success)
	return result, nil
}
