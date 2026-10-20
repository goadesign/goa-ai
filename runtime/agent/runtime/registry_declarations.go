// Package runtime keeps the generated registry declarations of the operations an
// agent calls or exports.
//
// When a consumer binds its compiled tools to a tool registry, a planned call
// saves only the registration chosen for it, not the tool's declaration. Every
// later reader of that call (workflow steps, paused-run checks, child agents,
// and later turns that page old results) gets the declaration from generated
// code instead. Generated definitions supply it with WithRegistryDeclarations;
// RegisterAgent copies each definition's declarations into one runtime table
// keyed by tool name and rejects two registrations that disagree.
package runtime

import (
	"fmt"
	"maps"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

// WithRegistryDeclarations returns an immutable definition holding the
// generated registry declaration of each of its tools that has one. Generated
// code passes its aggregate lookup; tools without a declaration, such as
// private page operations, are skipped. A declaration must name the tool it is
// returned for and must not carry a provider Agent target, because the
// selected registration supplies that target.
func (d AgentDefinition) WithRegistryDeclarations(lookup func(tools.Ident) (*genregistry.ToolSchema, bool)) AgentDefinition {
	if lookup == nil || d.declarations != nil {
		panic("runtime: agent registry declarations must be supplied exactly once")
	}
	declarations := make(map[tools.Ident]*genregistry.ToolSchema, len(d.specs))
	for _, spec := range d.specs {
		declaration, ok := lookup(spec.Name)
		if !ok {
			continue
		}
		if declaration == nil || tools.Ident(declaration.Name) != spec.Name || declaration.ConsumerContract == nil {
			panic(fmt.Sprintf("runtime: registry declaration for %q is incomplete", spec.Name))
		}
		if declaration.ConsumerContract.Agent != nil {
			panic(fmt.Sprintf("runtime: generated registry declaration for %q must not select a provider", spec.Name))
		}
		declarations[spec.Name] = declaration
	}
	d.declarations = declarations
	d.agents = maps.Clone(d.agents)
	d.agents[d.route.ID] = d
	return d
}

// registryDeclaration returns the generated declaration of one of this
// definition's tools, or false when generated code supplied none.
func (d AgentDefinition) registryDeclaration(name tools.Ident) (*genregistry.ToolSchema, bool) {
	declaration, ok := d.declarations[name]
	return declaration, ok
}

// registryDeclaration returns the generated declaration that any registered
// agent supplied for one tool name, or false when none did. Code that has no
// consuming definition uses it, as it uses toolSpec for specifications.
func (r *Runtime) registryDeclaration(name tools.Ident) (*genregistry.ToolSchema, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	declaration, ok := r.registryDeclarations[name]
	return declaration, ok
}

// validateRegistryDeclarations rejects a definition whose declaration for a
// tool differs from one an earlier registration supplied for the same name.
// It reads only; addRegistryDeclarationsLocked commits the accepted values.
func (r *Runtime) validateRegistryDeclarations(definition AgentDefinition) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, declaration := range definition.declarations {
		existing, ok := r.registryDeclarations[name]
		if !ok {
			continue
		}
		same, err := sameRegistryDeclaration(existing, declaration)
		if err != nil {
			return err
		}
		if !same {
			return fmt.Errorf("%w: tool %q has two different registry declarations", ErrInvalidConfig, name)
		}
	}
	return nil
}

// addRegistryDeclarationsLocked records a validated definition's declarations.
// The caller holds r.mu for writing.
func (r *Runtime) addRegistryDeclarationsLocked(definition AgentDefinition) {
	if r.registryDeclarations == nil {
		r.registryDeclarations = make(map[tools.Ident]*genregistry.ToolSchema, len(definition.declarations))
	}
	for name, declaration := range definition.declarations {
		if _, exists := r.registryDeclarations[name]; !exists {
			r.registryDeclarations[name] = declaration
		}
	}
}

// sameRegistryDeclaration compares two declarations with the registry identity
// algorithm, which covers every schema and consumer-contract field.
func sameRegistryDeclaration(a, b *genregistry.ToolSchema) (bool, error) {
	left, err := toolcontract.Fingerprint(&genregistry.Toolset{Name: a.Name, Tools: []*genregistry.ToolSchema{a}})
	if err != nil {
		return false, err
	}
	right, err := toolcontract.Fingerprint(&genregistry.Toolset{Name: b.Name, Tools: []*genregistry.ToolSchema{b}})
	if err != nil {
		return false, err
	}
	return left == right, nil
}
