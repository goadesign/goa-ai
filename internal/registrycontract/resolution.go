// Package registrycontract validates registry responses and saved selected-call definitions.
// Generated transport validators own wire constraints. The checks here own
// tool identity, supported execution, and pagination relationships.
package registrycontract

import (
	"errors"
	"fmt"
	"slices"

	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
	"google.golang.org/protobuf/encoding/protojson"
)

type (
	// Resolution contains one validated registration and its compiled contracts.
	// It is private to runtime integration and is never a model-facing catalog.
	Resolution struct {
		Registered *genregistry.ResolvedToolset
		Specs      map[tools.Ident]tools.ToolSpec
		byName     map[tools.Ident]*genregistry.ToolSchema
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
// The caller separately checks that the agent still consumes this source.
func Read(binding *tools.RegistryBinding) (*Resolution, error) {
	if binding == nil || binding.Registry == "" {
		return nil, errors.New("selected tool registry is required")
	}
	var message genregistrypb.ResolveToolsetResponse
	if err := protojson.Unmarshal(binding.Resolution, &message); err != nil {
		return nil, fmt.Errorf("decode selected registry tool: %w", err)
	}
	if err := genregistryclient.ValidateResolveToolsetResponse(&message); err != nil {
		return nil, fmt.Errorf("selected registry tool: %w", err)
	}
	return Resolve(genregistryclient.NewResolveToolsetResult(&message))
}

// Select retains just the chosen definition and its fixed pagination partner.
// The token still identifies the complete registration from which they came.
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
	toolset := *r.Registered.Toolset
	toolset.Tools = make([]*genregistry.ToolSchema, len(names))
	for index, selected := range names {
		toolset.Tools[index] = r.byName[selected]
	}
	encoded, err := protojson.Marshal(genregistryserver.NewProtoResolveToolsetResponse(&genregistry.ResolvedToolset{
		Toolset: &toolset, RegistrationToken: r.Registered.RegistrationToken,
	}))
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
