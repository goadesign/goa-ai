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
	result := &Resolution{
		Registered: registered,
		Specs:      make(map[tools.Ident]tools.ToolSpec, len(registered.Toolset.Tools)),
		byName:     make(map[tools.Ident]*genregistry.ToolSchema, len(registered.Toolset.Tools)),
	}
	for _, declaration := range registered.Toolset.Tools {
		name := tools.Ident(declaration.Name)
		if name.Toolset() == "" || name.Tool() == "" {
			return nil, fmt.Errorf("registry tool %q must be qualified", name)
		}
		if _, exists := result.Specs[name]; exists {
			return nil, fmt.Errorf("registry resolution repeats tool %q", name)
		}
		spec, err := toolcontract.Compile(declaration)
		if err != nil {
			return nil, err
		}
		result.Specs[name] = spec
		result.byName[name] = declaration
	}
	for _, spec := range result.Specs {
		if err := result.validatePaging(spec); err != nil {
			return nil, err
		}
	}
	return result, nil
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
func (r *Resolution) validatePaging(spec tools.ToolSpec) error {
	if spec.Bounds == nil || spec.Bounds.Paging == nil {
		return nil
	}
	paging := spec.Bounds.Paging
	if paging.SourceTool != "" {
		source, ok := r.Specs[paging.SourceTool]
		if !ok || source.Bounds == nil || source.Bounds.Paging == nil ||
			source.Bounds.Paging.ContinueTool != spec.Name || paging.ContinueTool != spec.Name {
			return fmt.Errorf("registry continuation %q has no matching source %q", spec.Name, paging.SourceTool)
		}
	} else if paging.ContinueTool != "" && paging.ContinueTool != spec.Name {
		target, ok := r.Specs[paging.ContinueTool]
		if !ok || target.Bounds == nil || target.Bounds.Paging == nil ||
			target.Bounds.Paging.SourceTool != spec.Name {
			return fmt.Errorf("registry query %q has no matching continuation %q", spec.Name, paging.ContinueTool)
		}
	}
	return nil
}
