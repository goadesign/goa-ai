package runtime

// Saved calls describe work that already happened. Before constructing today's
// page actions, this file removes finished historical chains and checks whether
// unfinished historical calls still have executable input contracts. Current
// work always reaches the normal strict continuation validation unchanged.

import (
	"errors"
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// historicalPagingKey keeps an exact source call within its saved registration.
	historicalPagingKey struct {
		call, registry, token string
	}

	// historicalPagingChain retains original outputs and whether current work
	// depends on them. It never changes saved input, result, or cursor bytes.
	historicalPagingChain struct {
		outputs []*planner.ToolOutput
		current bool
	}
)

// selectHistoricalContinuations reads successful pages in their saved order.
// Explicit source IDs close old chains even when their tool names have changed.
// Only prior-run evidence may become ineligible; current calls and results keep
// their existing validation, including pages that consume an earlier run's cursor.
func (r *Runtime) selectHistoricalContinuations(input *resolvedPlanActivityInput, outputs, current []*planner.ToolOutput, specs map[tools.Ident]tools.ToolSpec) ([]*planner.ToolOutput, error) {
	currentKeys := make(map[continuationOutputKey]struct{}, len(current))
	for _, output := range current {
		currentKeys[continuationOutputKey{output.CallRunID, output.ResultRunID, output.ToolCallID}] = struct{}{}
	}
	chains := make(map[historicalPagingKey]*historicalPagingChain)
	seen := make(map[historicalPagingKey]struct{})
	keys := make(map[*planner.ToolOutput]historicalPagingKey, len(outputs))
	for _, output := range outputs {
		if output.Failure != nil {
			continue
		}
		_, explicitCurrent := currentKeys[continuationOutputKey{output.CallRunID, output.ResultRunID, output.ToolCallID}]
		isCurrent := explicitCurrent || output.CallRunID == input.RunID || output.ResultRunID == input.RunID
		if isCurrent {
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
		key := historicalPagingKey{call: output.ToolCallID}
		if output.Registry != nil {
			token, err := registrycontract.RegistrationToken(output.Registry)
			if err != nil {
				return nil, err
			}
			key.registry = output.Registry.Registry
			key.token = token
		}
		if key.call == "" {
			return nil, fmt.Errorf("runtime: historical paging call has an empty tool call id")
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("runtime: duplicate historical paging call %q", key.call)
		}
		seen[key] = struct{}{}
		if output.ContinuationRootToolCallID != "" {
			key.call = output.ContinuationRootToolCallID
			chain, exists := chains[key]
			if !exists {
				return nil, fmt.Errorf("runtime: historical continuation references unknown source tool call %q in its registration", key.call)
			}
			previous := chain.outputs[len(chain.outputs)-1]
			if previous.Bounds == nil || previous.Bounds.NextCursor == nil {
				return nil, fmt.Errorf("runtime: historical continuation advances completed source tool call %q", key.call)
			}
		} else {
			chains[key] = &historicalPagingChain{}
		}
		chain := chains[key]
		chain.outputs = append(chain.outputs, output)
		chain.current = chain.current || isCurrent
		keys[output] = key
	}

	eligible := make(map[historicalPagingKey]bool, len(chains))
	for key, chain := range chains {
		if chain.current {
			eligible[key] = true
			continue
		}
		head := chain.outputs[len(chain.outputs)-1]
		if head.Bounds == nil || head.Bounds.NextCursor == nil {
			continue
		}
		eligible[key] = true
		if head.Registry != nil {
			// Registered calls retain their exact old contracts. The ordinary
			// action builder checks those contracts and current authorization.
			continue
		}
		var err error
		eligible[key], err = historicalPagingExecutable(chain.outputs, specs)
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
