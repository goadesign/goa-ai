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
// Before a new page uses this registration, verifyPrevious must verify that the
// saved private toolset route and exact token belong to this same logical
// resource. An owner read failure stops planning; accepted calls keep their
// original registration and never call verifyPrevious.
func (c *RegistryCatalog) BindStaticService(registry string, expected *genregistry.Toolset, selected *genregistry.ResolvedToolset, verifyPrevious func(context.Context, string, string) error, names ...tools.Ident) error {
	if verifyPrevious == nil {
		return fmt.Errorf("static service binding requires verification of previous registrations")
	}
	if len(names) == 0 {
		return fmt.Errorf("static service binding requires explicit tool names")
	}
	if _, err := c.runtime.registryConnection(registry); err != nil {
		return err
	}
	encoded, err := genregistry.EncodeResolvedToolset(selected)
	if err != nil {
		return err
	}
	selected, err = genregistry.DecodeResolvedToolset(encoded)
	if err != nil {
		return err
	}
	resolved, err := registrycontract.ResolveCompiled(selected)
	if err != nil {
		return err
	}
	if expected == nil {
		return fmt.Errorf("static service binding requires the generated toolset declaration")
	}
	declaration := *expected
	declaration.Name = selected.Toolset.Name
	expectedFingerprint, err := toolcontract.Fingerprint(&declaration)
	if err != nil {
		return err
	}
	selectedFingerprint, err := toolcontract.Fingerprint(selected.Toolset)
	if err != nil {
		return err
	}
	if expectedFingerprint != selectedFingerprint {
		return fmt.Errorf("static service binding differs from the complete generated declaration")
	}
	version := ""
	if selected.Toolset.Version != nil {
		version = string(*selected.Toolset.Version)
	}
	if c.sources == nil || !c.sources.Allows(registry, selected.Toolset.Name, version) {
		return fmt.Errorf("registry %q toolset %q is outside this agent's declared consumption", registry, selected.Toolset.Name)
	}
	for _, declaration := range selected.Toolset.Tools {
		if declaration.ConsumerContract.Kind != staticServiceKind {
			return fmt.Errorf("static service binding cannot include %s tool %q", declaration.ConsumerContract.Kind, declaration.Name)
		}
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
	if err := c.requireCompiledRestore(registry, resolved, names); err != nil {
		return err
	}
	for _, name := range names {
		declaration, err := resolved.Declaration(name)
		if err != nil {
			return err
		}
		c.selections[name] = registrySelection{registry: registry, resolution: resolved, verifyPrevious: verifyPrevious}
		c.labels[name] = slices.Clone(declaration.ConsumerContract.RequiredLabels)
	}
	return nil
}

// requireCompiledRestore checks that a call to each bound tool can be saved
// without its declaration and restored from this definition's generated
// declarations. Later workflow steps, paused runs, child agents, and later
// turns restore saved calls the same way, so a definition generated without
// registry declarations is rejected here, before any call is planned.
func (c *RegistryCatalog) requireCompiledRestore(registry string, resolved *registrycontract.Resolution, names []tools.Ident) error {
	for _, name := range names {
		binding, err := resolved.Select(registry, name)
		if err != nil {
			return err
		}
		if _, err := registrycontract.Read(binding, c.definition.registryDeclaration); err != nil {
			return fmt.Errorf("tool %q cannot be restored from this agent's generated registry declarations; regenerate the agent: %w", name, err)
		}
	}
	return nil
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
