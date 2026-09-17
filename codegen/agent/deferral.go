// Package codegen writes each consumer's loading choice into its own generated
// agent definition. Registry loading choices belong to generated source reads.
package codegen

import (
	"fmt"
	"slices"
)

// agentDeferral resolves each consumer's loading choice against its complete
// compiled tools, including Goa-backed MCP tools. Unknown local names fail
// generation; accepted names become exact runtime tool IDs.
func agentDeferral(agent *AgentData) ([]string, error) {
	var names []string
	for _, toolset := range agent.UsedToolsets {
		if toolset.IsRegistryBacked {
			continue
		}
		if toolset.Deferred {
			for _, tool := range toolset.Tools {
				names = append(names, tool.QualifiedName)
			}
			continue
		}
		for _, name := range toolset.Expr.DeferredTools {
			index := slices.IndexFunc(toolset.Tools, func(tool *ToolData) bool {
				return tool.Name == name
			})
			if index == -1 {
				return nil, fmt.Errorf("agent %q toolset %q: Deferred selects unknown tool %q", agent.ID, toolset.Name, name)
			}
			names = append(names, toolset.Tools[index].QualifiedName)
		}
	}
	slices.Sort(names)
	return names, nil
}
