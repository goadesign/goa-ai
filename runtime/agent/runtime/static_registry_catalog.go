// Package runtime attaches exact service registrations to generated static tools.
// The selected declaration supplies admission identity; generated definitions
// continue to own model names, codecs, input preparation, loading and execution.
package runtime

import (
	"context"
	"fmt"
	"slices"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

const staticServiceKind = "service"

// BindStaticService attaches an owner-selected registration to explicitly named
// generated service tools. The complete expected declaration must match, except
// its toolset name may be replaced by the selected route. No catalog is read.
// Before a new page uses this registration, readPrevious returns the exact old
// declarations and proves that the saved route and token belong to this same
// logical resource. An owner read failure stops planning; accepted calls keep their
// original registration and never call readPrevious. The reader returns the
// exact owner's ResolvedToolset so saved fingerprints use the old declarations.
func (c *RegistryCatalog) BindStaticService(registry string, expected *genregistry.Toolset, selected *genregistry.ResolvedToolset, readPrevious func(context.Context, string, string) (*genregistry.ResolvedToolset, error), names ...tools.Ident) error {
	if readPrevious == nil {
		return fmt.Errorf("static service binding requires a reader for previous registrations")
	}
	if len(names) == 0 {
		return fmt.Errorf("static service binding requires explicit tool names")
	}
	if _, err := c.runtime.registryConnection(registry); err != nil {
		return err
	}
	resolved, err := compileStaticServiceDeclarations(expected, selected)
	if err != nil {
		return err
	}
	selected = resolved.Registered
	version := ""
	if selected.Toolset.Version != nil {
		version = string(*selected.Toolset.Version)
	}
	if c.sources == nil || !c.sources.Allows(registry, selected.Toolset.Name, version) {
		return fmt.Errorf("registry %q toolset %q is outside this agent's declared consumption", registry, selected.Toolset.Name)
	}
	if err := validateStaticServiceKinds(resolved); err != nil {
		return err
	}
	for index, name := range names {
		if slices.Contains(names[:index], name) {
			return fmt.Errorf("static service binding repeats tool %q", name)
		}
		if _, exists := c.selections[name]; exists {
			return fmt.Errorf("tool %q already has a selected registration", name)
		}
		spec, exists := c.definition.specByName[name]
		if !exists {
			return fmt.Errorf("tool %q is not static in this consuming definition", name)
		}
		if err := validateStaticServiceSpec(spec, resolved); err != nil {
			return err
		}
	}
	// Read every saved selection before storing any of them, so a later failure
	// leaves this activity's selected registrations and labels unchanged.
	restored, err := c.restoreCompiledSelections(registry, resolved, names)
	if err != nil {
		return err
	}
	return c.installStaticServiceSelections(registry, restored, readPrevious, names)
}

// restoreCompiledSelections saves each bound tool's registration and reads it
// with this definition's generated declarations. Missing or changed declarations
// return an error before the caller changes its catalog. Results follow names
// order, and each contains the called operation and its fixed paging partner.
func (c *RegistryCatalog) restoreCompiledSelections(
	registry string,
	resolved *registrycontract.Resolution,
	names []tools.Ident,
) ([]*registrycontract.Resolution, error) {
	restored := make([]*registrycontract.Resolution, len(names))
	for index, name := range names {
		binding, err := resolved.Select(registry, name)
		if err != nil {
			return nil, err
		}
		restored[index], err = registrycontract.Read(binding, c.definition.registryDeclaration)
		if err != nil {
			return nil, fmt.Errorf("tool %q cannot be restored from this agent's generated registry declarations; regenerate the agent: %w", name, err)
		}
	}
	return restored, nil
}

// SelectedOperation returns an owned copy of the native operation retained by
// this run. Direct and registered runs use their original declaration; absence
// or malformed saved context fails without reading the current registry.
func (c *RegistryCatalog) SelectedOperation() (*genregistry.ToolSchema, error) {
	selected := c.runContext.Operation
	if selected != nil && c.runContext.ToolRegistry != nil {
		return nil, fmt.Errorf("selected operation cannot include both direct and registered declarations")
	}
	if c.runContext.ToolRegistry != nil {
		resolved, err := registrycontract.Read(c.runContext.ToolRegistry, c.registryDeclaration)
		if err != nil {
			return nil, err
		}
		selected, err = resolved.Declaration(c.runContext.Tool)
		if err != nil {
			return nil, err
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("run has no selected native operation")
	}
	if _, err := compileAgentOperation(selected, c.definition.route.ID); err != nil {
		return nil, err
	}
	if c.runContext.ToolRegistry == nil && tools.Ident(selected.Name) != c.runContext.Tool {
		return nil, fmt.Errorf("selected operation name differs from the run's operation")
	}
	encoded, err := genregistry.EncodeToolSchema(selected)
	if err != nil {
		return nil, err
	}
	return genregistry.DecodeToolSchema(encoded)
}

// validateStaticServiceSpec compares saved portable policy and schemas with the
// owning generated tool. Local-only semantics and native Agent tools cannot use
// a service registration as their executor.
func validateStaticServiceSpec(spec tools.ToolSpec, resolved *registrycontract.Resolution) error {
	declaration, err := resolved.Declaration(spec.Name)
	if err != nil {
		return err
	}
	if resolved.Source != nil || declaration.ConsumerContract.Kind != staticServiceKind || !equivalentToolSpec(spec, resolved.Specs[spec.Name]) {
		return fmt.Errorf("saved service tool %q differs from this agent's static contract", spec.Name)
	}
	return nil
}

// compileStaticServiceDeclarations checks the complete raw inputs using the
// generated JSON copy and existing fingerprint rules. Callers receive an owned
// selected declaration only when every operation passed those checks.
func compileStaticServiceDeclarations(expected *genregistry.Toolset, selected *genregistry.ResolvedToolset) (*registrycontract.Resolution, error) {
	encoded, err := genregistry.EncodeResolvedToolset(selected)
	if err != nil {
		return nil, err
	}
	selected, err = genregistry.DecodeResolvedToolset(encoded)
	if err != nil {
		return nil, err
	}
	resolved, err := registrycontract.ResolveCompiled(selected)
	if err != nil {
		return nil, err
	}
	if expected == nil {
		return nil, fmt.Errorf("static service binding requires the generated toolset declaration")
	}
	declaration := *expected
	declaration.Name = selected.Toolset.Name
	expectedFingerprint, err := toolcontract.Fingerprint(&declaration)
	if err != nil {
		return nil, err
	}
	selectedFingerprint, err := toolcontract.Fingerprint(selected.Toolset)
	if err != nil {
		return nil, err
	}
	if expectedFingerprint != selectedFingerprint {
		return nil, fmt.Errorf("static service binding differs from the complete generated declaration")
	}
	return resolved, nil
}

// validateStaticServiceKinds rejects a complete checked declaration containing
// another execution kind, so a service binding cannot select an Agent provider.
func validateStaticServiceKinds(resolved *registrycontract.Resolution) error {
	for _, declaration := range resolved.Registered.Toolset.Tools {
		if declaration.ConsumerContract.Kind != staticServiceKind {
			return fmt.Errorf("static service binding cannot include %s tool %q", declaration.ConsumerContract.Kind, declaration.Name)
		}
	}
	return nil
}

// installStaticServiceSelections receives all restored operations after their
// checks succeed. It stores each selection and its copied labels on this
// activity's catalog with the reader supplied for this activity.
func (c *RegistryCatalog) installStaticServiceSelections(registry string, restored []*registrycontract.Resolution, readPrevious func(context.Context, string, string) (*genregistry.ResolvedToolset, error), names []tools.Ident) error {
	for index, name := range names {
		declaration, err := restored[index].Declaration(name)
		if err != nil {
			return err
		}
		c.selections[name] = registrySelection{registry: registry, resolution: restored[index], readPrevious: readPrevious}
		c.labels[name] = slices.Clone(declaration.ConsumerContract.RequiredLabels)
	}
	return nil
}
