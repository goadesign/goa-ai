// Package registrycontract validates registry responses and saved selected-call definitions.
// Generated transport validators own wire constraints. The checks here own
// tool identity, supported execution, and pagination relationships.
package registrycontract

import (
	"errors"
	"fmt"
	"slices"

	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

type (
	// Resolution contains one validated registration and its compiled contracts.
	// It is private to runtime integration and is never a model-facing catalog.
	Resolution struct {
		// Registered contains the unchanged provider declarations and exact token.
		Registered *genregistry.ResolvedToolset
		// Source is present only for an owner-selected registered resource.
		// The exact route and token remain tied to Registered.
		Source *genregistry.CatalogSource
		// Specs contains the validated contracts under their consumer names.
		Specs  map[tools.Ident]tools.ToolSpec
		byName map[tools.Ident]*genregistry.ToolSchema
		// compiled is true when the consumer's generated code declares these
		// operations, so Select saves only the registration chosen for a call.
		compiled bool
	}
)

// Resolve checks one catalog response and compiles its portable definitions.
// Every tool must support service or native Agent execution; partial catalogs are errors.
func Resolve(registered *genregistry.ResolvedToolset) (*Resolution, error) {
	if registered == nil {
		return nil, errors.New("registry resolution is required")
	}
	if err := genregistryclient.ValidateResolveToolsetResponse(
		genregistryserver.NewProtoResolveToolsetResponse(registered),
	); err != nil {
		return nil, fmt.Errorf("registry resolution: %w", err)
	}
	specs, err := Compile(registered.Toolset.Tools)
	if err != nil {
		return nil, err
	}
	byName := make(map[tools.Ident]*genregistry.ToolSchema, len(registered.Toolset.Tools))
	for _, declaration := range registered.Toolset.Tools {
		byName[tools.Ident(declaration.Name)] = declaration
	}
	return &Resolution{Registered: registered, Specs: specs, byName: byName}, nil
}

// Compile validates a complete collection of portable tool declarations before
// registration. Tool names must be qualified and unique, and pagination partners
// must agree. No registration token or timestamp is needed to check definitions.
func Compile(declarations []*genregistry.ToolSchema) (map[tools.Ident]tools.ToolSpec, error) {
	specs := make(map[tools.Ident]tools.ToolSpec, len(declarations))
	for _, declaration := range declarations {
		name := tools.Ident(declaration.Name)
		if name.Toolset() == "" || name.Tool() == "" {
			return nil, fmt.Errorf("registry tool %q must be qualified", name)
		}
		if _, exists := specs[name]; exists {
			return nil, fmt.Errorf("registry resolution repeats tool %q", name)
		}
		spec, err := toolcontract.Compile(declaration)
		if err != nil {
			return nil, err
		}
		specs[name] = spec
	}
	for _, spec := range specs {
		if err := validatePaging(specs, spec); err != nil {
			return nil, err
		}
	}
	return specs, nil
}

// Read validates a saved binding without consulting the current remote catalog.
// Direct and registered selections contain their declarations. Compiled
// selections take them from compiled, the reader's generated declarations, and
// return ErrCompiledContractChanged when those differ from the saved
// fingerprint. The caller separately checks that the agent still consumes this
// source.
func Read(binding *tools.RegistryBinding, compiled CompiledDeclarations) (*Resolution, error) {
	selected, err := decodeSelection(binding)
	if err != nil {
		return nil, err
	}
	if direct, ok := selected.Selection.AsDirect(); ok {
		return Resolve(direct)
	}
	if registered, ok := selected.Selection.AsRegistered(); ok {
		return ResolveRegistered(binding.Registry, registered.Source, registered.Resolved)
	}
	if service, ok := selected.Selection.AsCompiledService(); ok {
		return readCompiled(service, nil, compiled)
	}
	agent, _ := selected.Selection.AsCompiledAgent()
	return readCompiled(agent.Selection, agent.Agent, compiled)
}

// Select retains just the chosen definition and its fixed pagination partner.
// The token still identifies the complete registration from which they came.
// For a compiled resolution it saves only the registration chosen for those
// operations; the reader supplies their declarations from generated code.
func (r *Resolution) Select(registry string, name tools.Ident) (*tools.RegistryBinding, error) {
	spec, ok := r.Specs[name]
	if !ok {
		return nil, fmt.Errorf("registry selection contains no tool %q", name)
	}
	names := []tools.Ident{name}
	if paging := spec.Bounds; paging != nil && paging.Paging != nil {
		for _, partner := range []tools.Ident{paging.Paging.SourceTool, paging.Paging.ContinueTool} {
			if partner != "" && !slices.Contains(names, partner) {
				names = append(names, partner)
			}
		}
	}
	slices.Sort(names)
	if r.compiled {
		selection, err := r.selectCompiled(names)
		if err != nil {
			return nil, err
		}
		return encodeBinding(registry, selection)
	}
	toolset := *r.Registered.Toolset
	toolset.Tools = make([]*genregistry.ToolSchema, len(names))
	for index, selected := range names {
		toolset.Tools[index] = r.byName[selected]
	}
	retained := &genregistry.ResolvedToolset{
		Toolset: &toolset, RegistrationToken: r.Registered.RegistrationToken,
	}
	selection := genregistry.NewRegistrySelectionKindDirect(retained)
	if r.Source != nil {
		selection = genregistry.NewRegistrySelectionKindRegistered(&genregistry.RegisteredToolsetSelection{
			Source: r.Source, Resolved: retained,
		})
	}
	return encodeBinding(registry, selection)
}

// encodeBinding saves one selection under the registry connection that owns it.
func encodeBinding(registry string, selection genregistry.RegistrySelectionKind) (*tools.RegistryBinding, error) {
	encoded, err := genregistry.EncodeRegistrySelection(&genregistry.RegistrySelection{Selection: selection})
	if err != nil {
		return nil, fmt.Errorf("encode selected registry tool: %w", err)
	}
	return &tools.RegistryBinding{Registry: registry, Resolution: encoded}, nil
}

// validatePaging rejects incomplete or contradictory source/continuation
// pairs before the model can select a query whose cursor cannot be advanced.
func validatePaging(specs map[tools.Ident]tools.ToolSpec, spec tools.ToolSpec) error {
	if spec.Bounds == nil || spec.Bounds.Paging == nil {
		return nil
	}
	paging := spec.Bounds.Paging
	if paging.SourceTool != "" {
		source, ok := specs[paging.SourceTool]
		if !ok || source.Bounds == nil || source.Bounds.Paging == nil ||
			source.Bounds.Paging.ContinueTool != spec.Name || paging.ContinueTool != spec.Name {
			return fmt.Errorf("registry continuation %q has no matching source %q", spec.Name, paging.SourceTool)
		}
	} else if paging.ContinueTool != "" && paging.ContinueTool != spec.Name {
		target, ok := specs[paging.ContinueTool]
		if !ok || target.Bounds == nil || target.Bounds.Paging == nil ||
			target.Bounds.Paging.SourceTool != spec.Name {
			return fmt.Errorf("registry query %q has no matching continuation %q", spec.Name, paging.ContinueTool)
		}
	}
	return nil
}

// Declaration returns the original provider operation for a consumer name.
// Callers use it for execution and provider-owned policy, never schema renaming.
func (r *Resolution) Declaration(name tools.Ident) (*genregistry.ToolSchema, error) {
	declaration, ok := r.byName[name]
	if !ok {
		return nil, fmt.Errorf("registry selection contains no declaration for %q", name)
	}
	return declaration, nil
}
