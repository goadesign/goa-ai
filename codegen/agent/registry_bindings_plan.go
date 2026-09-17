// Package codegen specializes registry consumption from the evaluated design.
// Generated methods contain exact source reads and permission predicates; the
// runtime supplies clients and handles only the catalog returned by those reads.
package codegen

import (
	"cmp"
	"slices"

	agentir "goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
)

type (
	// registrySourceData is one statically declared read. An empty ToolsetName
	// selects the whole-registry template during generation, never at runtime.
	registrySourceData struct {
		RegistryName string
		ToolsetName  string
		Version      string
		Deferred     bool
	}

	// agentRegistrySourcesData owns the generated methods for one agent,
	// including child definitions embedded in a caller's generated package.
	agentRegistrySourcesData struct {
		AgentID     string
		Sources     []*registrySourceData
		TypeName    string
		declaration *goacodegen.NameDeclaration
	}
)

// planRegistrySources collects each agent's own consumption without merging
// child permissions into the parent. Source order is fixed during generation.
func planRegistrySources(root *agentir.Agent, childIDs []string) []*agentRegistrySourcesData {
	agents := make([]*agentir.Agent, 1, 1+len(childIDs))
	agents[0] = root
	for _, id := range childIDs {
		agents = append(agents, agentByID(root, id))
	}
	var result []*agentRegistrySourcesData
	for _, agent := range agents {
		data := &agentRegistrySourcesData{AgentID: agent.ID}
		for _, use := range agent.Expr.Registries {
			data.Sources = append(data.Sources, &registrySourceData{RegistryName: use.Registry.Name, Deferred: use.Deferred})
		}
		for _, ref := range agent.UsedToolsets {
			if ref.Provider == nil || ref.Provider.Kind != agentexpr.ProviderRegistry {
				continue
			}
			source := ref.Provider.Registry
			data.Sources = append(data.Sources, &registrySourceData{
				RegistryName: source.RegistryName, ToolsetName: source.ToolsetName,
				Version: source.Version, Deferred: ref.Deferred,
			})
		}
		if len(data.Sources) == 0 {
			continue
		}
		slices.SortFunc(data.Sources, func(a, b *registrySourceData) int {
			return cmp.Or(cmp.Compare(a.RegistryName, b.RegistryName), cmp.Compare(a.ToolsetName, b.ToolsetName))
		})
		result = append(result, data)
	}
	return result
}

// registrySourcesFor selects one already-planned generated type. Missing data
// means this agent has only compiled tools and emits no registry methods.
func registrySourcesFor(agentID string, sources []*agentRegistrySourcesData) *agentRegistrySourcesData {
	for _, source := range sources {
		if source.AgentID == agentID {
			return source
		}
	}
	return nil
}
