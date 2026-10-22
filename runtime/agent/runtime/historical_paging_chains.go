package runtime

// Saved page relationships determine whether a query can still fetch a page.
// Checkpoint validation and planner action selection share this analysis so a
// materialized final page closes the same source in both paths.

import (
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent/planner"
)

// historicalPagingKey identifies one source call within its saved registration.
type historicalPagingKey struct {
	call, registry, token string
}

// historicalPagingChains groups successful saved outputs in recorded order.
// A final page without a next cursor closes its exact source. Failed attempts
// cannot close a chain, and pages cannot advance a source that already closed.
func historicalPagingChains(outputs []*planner.ToolOutput) (map[historicalPagingKey][]*planner.ToolOutput, map[*planner.ToolOutput]historicalPagingKey, error) {
	chains := make(map[historicalPagingKey][]*planner.ToolOutput)
	seen := make(map[historicalPagingKey]bool)
	keys := make(map[*planner.ToolOutput]historicalPagingKey, len(outputs))
	for _, output := range outputs {
		if output.Failure != nil {
			continue
		}
		key := historicalPagingKey{call: output.ToolCallID}
		if output.Registry != nil {
			token, err := registrycontract.RegistrationToken(output.Registry)
			if err != nil {
				return nil, nil, err
			}
			key.registry, key.token = output.Registry.Registry, token
		}
		if key.call == "" {
			return nil, nil, fmt.Errorf("runtime: historical paging call has an empty tool call id")
		}
		if seen[key] {
			return nil, nil, fmt.Errorf("runtime: duplicate historical paging call %q", key.call)
		}
		seen[key] = true
		if output.ContinuationRootToolCallID != "" {
			key.call = output.ContinuationRootToolCallID
			chain, exists := chains[key]
			if !exists {
				return nil, nil, fmt.Errorf("runtime: historical continuation references unknown source tool call %q in its registration", key.call)
			}
			previous := chain[len(chain)-1]
			if previous.Bounds == nil || previous.Bounds.NextCursor == nil {
				return nil, nil, fmt.Errorf("runtime: historical continuation advances completed source tool call %q", key.call)
			}
		}
		chains[key] = append(chains[key], output)
		keys[output] = key
	}
	return chains, keys, nil
}
