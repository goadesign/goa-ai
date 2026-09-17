// Package codegen plans the discovered toolsets required to construct an agent.
// The generated input has one named field per consumed toolset, including those
// needed by reachable child agents. Discovery remains application startup work;
// generated definitions never read a mutable package-global catalog.
package codegen

import (
	"cmp"
	"slices"

	agentir "goa.design/goa-ai/codegen/ir"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
)

type (
	// registryBindingData identifies one startup input and its declared source.
	registryBindingData struct {
		// FieldName names the input in the generated RegistryToolsets struct.
		FieldName string
		// QualifiedName identifies the registry and remote toolset in errors.
		QualifiedName string
		// RegistryName identifies the declared catalog source.
		RegistryName string
		// ToolsetName identifies the provider's toolset within that catalog.
		ToolsetName string
		// Version is the optional design-time version requirement.
		Version string
		// References contains the local registration routes sharing this source.
		References []string
	}

	// registryBindingKey identifies one catalog contract shared across local
	// registration routes and reachable agents.
	registryBindingKey struct {
		registry string
		toolset  string
		version  string
	}

	// registryBindingsData contains the complete startup input for one generated
	// agent package. StaticToolNames prevents dynamic inputs from replacing
	// contracts already fixed by generation.
	registryBindingsData struct {
		// RegistryToolsetsType is the final generated startup input type name.
		RegistryToolsetsType string
		// RegistryBindings contains each distinct catalog contract exactly once.
		RegistryBindings []*registryBindingData
		// StaticToolNames contains identities already fixed by generated schemas.
		StaticToolNames []string
	}
)

const (
	registryToolsetsTypeName = "RegistryToolsets"
	registryRuntimePath      = "goa.design/goa-ai/runtime/registry"
	registryPolicyPath       = "goa.design/goa-ai/runtime/agent/policy"
)

// planRegistryBindings collects known registry references from the generated
// composition graph and assigns stable, distinct struct field names.
func planRegistryBindings(root *agentir.Agent, childIDs []string) []*registryBindingData {
	agents := make([]*agentir.Agent, 1, 1+len(childIDs))
	agents[0] = root
	for _, id := range childIDs {
		agents = append(agents, agentByID(root, id))
	}
	byName := make(map[registryBindingKey]*registryBindingData)
	for _, agent := range agents {
		for _, refs := range [][]*agentir.ToolsetRef{agent.UsedToolsets, agent.ExportedToolsets} {
			for _, ref := range refs {
				if ref.Provider == nil || ref.Provider.Kind != agentexpr.ProviderRegistry {
					continue
				}
				source := ref.Provider.Registry
				key := registryBindingKey{
					registry: source.RegistryName,
					toolset:  source.ToolsetName,
					version:  source.Version,
				}
				binding := byName[key]
				if binding == nil {
					binding = &registryBindingData{
						QualifiedName: source.RegistryName + "." + source.ToolsetName,
						RegistryName:  source.RegistryName,
						ToolsetName:   source.ToolsetName,
						Version:       source.Version,
					}
					byName[key] = binding
				}
				if !slices.Contains(binding.References, ref.QualifiedName) {
					binding.References = append(binding.References, ref.QualifiedName)
				}
			}
		}
	}
	bindings := make([]*registryBindingData, 0, len(byName))
	for _, binding := range byName {
		bindings = append(bindings, binding)
	}
	slices.SortFunc(bindings, func(a, b *registryBindingData) int {
		return cmp.Or(
			cmp.Compare(a.RegistryName, b.RegistryName),
			cmp.Compare(a.ToolsetName, b.ToolsetName),
			cmp.Compare(a.Version, b.Version),
		)
	})
	scope := goacodegen.NewNameScope()
	scope.Unique("Validate")
	for _, binding := range bindings {
		binding.FieldName = scope.Unique(goacodegen.Goify(binding.ToolsetName, true))
	}
	return bindings
}

// registryFileData attaches the declared type name and the tools already owned
// by compiled contracts to this package's dynamic startup inputs.
func (p *agentPackagePlan) registryFileData(root *AgentData, agents map[string]*AgentData) registryBindingsData {
	data := registryBindingsData{RegistryBindings: p.registryBindings}
	if len(p.registryBindings) == 0 {
		return data
	}
	data.RegistryToolsetsType = p.fixed[registryToolsetsTypeName].Name()
	names := make(map[string]struct{})
	all := []*AgentData{root}
	for _, id := range p.definitionAgentIDs {
		all = append(all, agents[id])
	}
	for _, agent := range all {
		for _, tool := range agent.Tools {
			names[tool.QualifiedName] = struct{}{}
		}
	}
	for name := range names {
		data.StaticToolNames = append(data.StaticToolNames, name)
	}
	slices.Sort(data.StaticToolNames)
	return data
}

// registryBindingsFor selects the generated startup fields used by one agent
// definition or its directly executable toolsets.
func registryBindingsFor(toolsets []*ToolsetData, bindings []*registryBindingData) []*registryBindingData {
	used := make(map[string]struct{}, len(toolsets))
	for _, toolset := range toolsets {
		if toolset.IsRegistryBacked {
			used[toolset.QualifiedName] = struct{}{}
		}
	}
	var selected []*registryBindingData
	for _, binding := range bindings {
		for _, reference := range binding.References {
			if _, ok := used[reference]; ok {
				selected = append(selected, binding)
				break
			}
		}
	}
	return selected
}
