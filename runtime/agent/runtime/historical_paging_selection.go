package runtime

// Saved calls describe work that already happened. Before constructing today's
// page actions, this file removes finished historical chains and checks whether
// unfinished historical calls still have executable input contracts. Current
// work always reaches the normal strict continuation validation unchanged.

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// selectHistoricalContinuations closes saved chains before checking current
// contracts. Live calls still receive current validation, including a final page.
func (r *Runtime) selectHistoricalContinuations(input *resolvedPlanActivityInput, outputs, current []*planner.ToolOutput, specs map[tools.Ident]tools.ToolSpec) ([]*planner.ToolOutput, error) {
	chains, keys, err := historicalPagingChains(outputs)
	if err != nil {
		return nil, err
	}
	currentKeys := make(map[continuationOutputKey]bool, len(current))
	for _, output := range current {
		currentKeys[continuationOutputKey{output.CallRunID, output.ResultRunID, output.ToolCallID}] = true
	}
	eligible := make(map[historicalPagingKey]bool, len(chains))
	for key, chain := range chains {
		head := chain[len(chain)-1]
		open := head.Bounds != nil && head.Bounds.NextCursor != nil
		var currentChain bool
		for _, output := range chain {
			live := output.CallRunID == input.RunID || output.ResultRunID == input.RunID
			retained := currentKeys[continuationOutputKey{output.CallRunID, output.ResultRunID, output.ToolCallID}]
			if !live && (!open || !retained) {
				continue
			}
			currentChain = true
			call := ToolCall{Name: output.Name, ToolCallID: output.ToolCallID, Registry: output.Registry}
			spec, exists, err := lookupCallSpec(call, r)
			if err != nil {
				return nil, err
			}
			if !exists {
				return nil, fmt.Errorf("runtime: current paging output references unregistered tool %q", output.Name)
			}
			if err := validateToolBoundsContract(spec, call, false, output.Bounds); err != nil {
				return nil, err
			}
		}
		if currentChain {
			eligible[key] = true
			continue
		}
		if !open {
			continue
		}
		eligible[key] = true
		if head.Registry != nil {
			// Registered calls retain their exact old contracts. The action
			// builder checks those contracts and current authorization.
			continue
		}
		eligible[key], err = historicalPagingExecutable(chain, specs)
		if err != nil {
			return nil, err
		}
	}
	selected := make([]*planner.ToolOutput, 0, len(outputs))
	for _, output := range outputs {
		if output.Failure != nil || eligible[keys[output]] {
			selected = append(selected, output)
		}
	}
	return selected, nil
}

// historicalPagingExecutable uses the current source and page contracts to
// build the actual next request. Changed arguments make an old action unavailable;
// inconsistent saved relationships and broken codecs still return an error.
func historicalPagingExecutable(outputs []*planner.ToolOutput, specs map[tools.Ident]tools.ToolSpec) (bool, error) {
	source, exists := specs[outputs[0].Name]
	if !exists || source.Bounds == nil || source.Bounds.Paging == nil {
		return false, nil
	}
	continuation, exists := specs[source.Bounds.Paging.ContinueTool]
	if !exists {
		return false, nil
	}
	composed, err := isComposedContinuationSpec(continuation)
	if err != nil {
		return false, err
	}
	if !composed && !isDedicatedContinuationSpec(continuation) {
		return false, nil
	}
	for _, output := range outputs {
		if !isContinuationOutput(continuation, output.Name) {
			return false, nil
		}
		spec := source
		if output.Name == continuation.Name {
			spec = continuation
		}
		if _, err := spec.ExecutionPayloadCodec.FromJSON(output.Payload); err != nil {
			return false, nil //nolint:nilerr // An old input that no longer fits makes this historical action unavailable.
		}
	}
	states, err := continuationStates(continuation, source, outputs)
	if err != nil {
		return false, err
	}
	for _, state := range states {
		if _, err := continuationPayload(continuation, state); err != nil {
			var mismatch continuationInputMismatch
			if errors.As(err, &mismatch) {
				return false, nil
			}
			return false, err
		}
	}
	return len(states) != 0, nil
}
