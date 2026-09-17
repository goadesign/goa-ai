// Package codegen writes each consumer's loading choice into its own generated
// agent definition. Registry loading choices belong to generated source reads.
package codegen

import "slices"

// agentDeferral returns the complete static deferred set during generation.
func agentDeferral(agent *AgentData) []string {
	var names []string
	for _, toolset := range agent.UsedToolsets {
		if !toolset.Deferred {
			continue
		}
		if toolset.IsRegistryBacked {
			continue
		}
		for _, tool := range toolset.Tools {
			names = append(names, tool.QualifiedName)
		}
	}
	slices.Sort(names)
	return names
}
