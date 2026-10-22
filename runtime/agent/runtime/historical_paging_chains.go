package runtime

// Saved page relationships determine whether a query can still fetch a page.
// Checkpoint validation and planner action selection share this analysis so a
// materialized final page closes the same source in both paths.

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/planner"
)

// historicalPagingChains groups successful saved outputs in recorded order.
// A source call ID keeps its pages together across provider registrations.
// A final page without a next cursor closes its exact source. Failed attempts
// cannot close a chain, and pages cannot advance a source that already closed.
func historicalPagingChains(outputs []*planner.ToolOutput) (map[string][]*planner.ToolOutput, map[*planner.ToolOutput]string, error) {
	chains := make(map[string][]*planner.ToolOutput)
	seen := make(map[string]bool)
	keys := make(map[*planner.ToolOutput]string, len(outputs))
	for _, output := range outputs {
		if output.Failure != nil {
			continue
		}
		key := output.ToolCallID
		if key == "" {
			return nil, nil, fmt.Errorf("runtime: historical paging call has an empty tool call id")
		}
		if seen[key] {
			return nil, nil, fmt.Errorf("runtime: duplicate historical paging call %q", key)
		}
		seen[key] = true
		if output.ContinuationRootToolCallID != "" {
			key = output.ContinuationRootToolCallID
			chain, exists := chains[key]
			if !exists {
				return nil, nil, fmt.Errorf("runtime: historical continuation references unknown source tool call %q", key)
			}
			previous := chain[len(chain)-1]
			if (previous.Registry == nil) != (output.Registry == nil) ||
				previous.Registry != nil && previous.Registry.Registry != output.Registry.Registry {
				return nil, nil, fmt.Errorf("runtime: historical continuation changes registry for source tool call %q", key)
			}
			if previous.Bounds == nil || previous.Bounds.NextCursor == nil {
				return nil, nil, fmt.Errorf("runtime: historical continuation advances completed source tool call %q", key)
			}
		}
		chains[key] = append(chains[key], output)
		keys[output] = key
	}
	return chains, keys, nil
}
