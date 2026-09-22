// Package runtime resolves the registry sources emitted by agent generation. Each
// catalog belongs to one planning activity; it never changes the runtime's
// registered static tools or another concurrent planning activity.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// RegistryTools defines an agent's registry consumption.
	// Generated implementations use sources known at generation time. Application
	// implementations may use RegistryCatalog.RunLabels for per-run selection.
	// Applications supply registry connections separately.
	RegistryTools interface {
		// Resolve adds the declared sources to one activity's catalog.
		Resolve(context.Context, *RegistryCatalog) error
		// Allows checks a saved call's source against the consuming design.
		// Registration membership and run policy remain separate checks.
		Allows(registry, toolset, version string) bool
	}

	// RegistryCatalog receives the source reads emitted by generated code.
	// Applications do not construct or retain it. Runtime planning owns its
	// lifetime and applies run policy before exposing any definition.
	RegistryCatalog struct {
		runtime     *Runtime
		definition  AgentDefinition
		runLabels   map[string]string
		sources     RegistryTools
		specs       map[tools.Ident]tools.ToolSpec
		definitions map[tools.Ident]*model.ToolDefinition
		selections  map[tools.Ident]registrySelection
		labels      map[tools.Ident][]string
	}

	registrySelection struct {
		registry   string
		resolution *registrycontract.Resolution
	}

	registryConnection struct {
		client *genregistry.Client
		pulse  pulsec.Client
	}
)

// RegisterRegistry installs already-constructed registry and result-stream
// clients under the name used by the design. It must precede Seal or any run.
// Registering the same name twice is an error, even for the same clients.
func (r *Runtime) RegisterRegistry(name string, client *genregistry.Client, pulse pulsec.Client) error {
	if name == "" || client == nil || pulse == nil {
		return errors.New("runtime: registry name, client, and result-stream client are required")
	}
	r.registrationMu.Lock()
	defer r.registrationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registrationClosed {
		return ErrRegistrationClosed
	}
	if _, exists := r.registries[name]; exists {
		return fmt.Errorf("runtime: registry %q is already registered", name)
	}
	if r.registries == nil {
		r.registries = make(map[string]registryConnection)
	}
	r.registries[name] = registryConnection{client: client, pulse: pulse}
	return nil
}

// WithRegistryTools returns an immutable definition with generated registry
// consumption attached. Static contracts and reachable child definitions keep
// their existing ownership; no remote catalog is read during construction.
func (d AgentDefinition) WithRegistryTools(sources RegistryTools) AgentDefinition {
	if sources == nil || d.registryTools != nil {
		panic("runtime: agent registry tools must be supplied exactly once")
	}
	d.registryTools = sources
	d.agents = maps.Clone(d.agents)
	d.agents[d.route.ID] = d
	return d
}

// IncludeToolset resolves one required registration. An empty version accepts
// the version currently published; a specified version must match exactly.
// Generated code supplies all arguments, including the model-loading choice.
func (c *RegistryCatalog) IncludeToolset(ctx context.Context, registry, name, version string, deferred bool) error {
	connection, err := c.runtime.registryConnection(registry)
	if err != nil {
		return err
	}
	ctx, span := c.runtime.tracer.Start(ctx, "registry.resolve_toolset",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("registry.name", registry), attribute.String("registry.toolset", name)))
	defer span.End()
	registered, err := connection.client.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: name})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "registry resolution failed")
		return fmt.Errorf("resolve registry %q toolset %q: %w", registry, name, err)
	}
	resolved, err := registrycontract.Resolve(registered)
	if err != nil {
		return fmt.Errorf("resolve registry %q toolset %q: %w", registry, name, err)
	}
	if registered.Toolset.Name != name {
		return fmt.Errorf("registry %q returned toolset %q for %q", registry, registered.Toolset.Name, name)
	}
	actualVersion := ""
	if registered.Toolset.Version != nil {
		actualVersion = string(*registered.Toolset.Version)
	}
	if version != "" && version != actualVersion {
		return fmt.Errorf("registry %q toolset %q requires version %q, got %q", registry, name, version, actualVersion)
	}
	if !c.sources.Allows(registry, name, actualVersion) {
		return fmt.Errorf("registry %q toolset %q is outside this agent's declared consumption", registry, name)
	}
	for _, declaration := range registered.Toolset.Tools {
		tool := tools.Ident(declaration.Name)
		if _, exists := c.specs[tool]; exists {
			return fmt.Errorf("registry %q toolset %q repeats tool %q already supplied to this agent", registry, name, tool)
		}
		spec := resolved.Specs[tool]
		if spec.IsAgentTool {
			if _, allowed := c.definition.agents[agent.Ident(spec.AgentID)]; !allowed {
				return fmt.Errorf("registry tool %q targets unconfigured Agent executor %q", tool, spec.AgentID)
			}
		}
		if IsGeneratedContinuationToolName(tool) {
			return fmt.Errorf("registry tool %q uses a reserved continuation name", tool)
		}
		definition, err := model.NewToolDefinitionFromSpec(spec)
		if err != nil {
			return fmt.Errorf("registry tool %q model contract: %w", tool, err)
		}
		definition.Deferred = deferred
		c.specs[tool] = spec
		c.definitions[tool] = definition
		c.selections[tool] = registrySelection{registry: registry, resolution: resolved}
		c.labels[tool] = slices.Clone(declaration.ConsumerContract.RequiredLabels)
	}
	return nil
}

// IncludeRegistry resolves every currently listed toolset through the same
// exact-registration operation. An empty registry succeeds; a failed read or
// a toolset removed between listing and resolution fails the activity.
func (c *RegistryCatalog) IncludeRegistry(ctx context.Context, registry string, deferred bool) error {
	connection, err := c.runtime.registryConnection(registry)
	if err != nil {
		return err
	}
	listed, err := connection.client.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
	if err != nil {
		return fmt.Errorf("list registry %q: %w", registry, err)
	}
	names := make([]string, len(listed.Toolsets))
	for index, toolset := range listed.Toolsets {
		names[index] = toolset.Name
	}
	slices.Sort(names)
	for _, name := range names {
		if err := c.IncludeToolset(ctx, registry, name, "", deferred); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) registryConnection(name string) (registryConnection, error) {
	r.mu.RLock()
	connection, ok := r.registries[name]
	r.mu.RUnlock()
	if !ok {
		return registryConnection{}, fmt.Errorf("runtime: registry %q is not registered", name)
	}
	return connection, nil
}

// resolveRegistryCatalog keeps static identities in the conflict check while
// adding dynamic definitions only to this activity's maps.
func (r *Runtime) resolveRegistryCatalog(ctx context.Context, definition AgentDefinition) (*RegistryCatalog, error) {
	catalog := r.newRegistryCatalog(definition)
	if definition.registryTools != nil {
		if err := definition.registryTools.Resolve(ctx, catalog); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

// planningCatalog reads the current sources once for an activity that can start
// new work. Final answers and explicit finalizers use only compiled contracts.
func (r *Runtime) planningCatalog(ctx context.Context, input *PlanActivityInput) (*RegistryCatalog, error) {
	registration, exists := r.agentByID(input.AgentID)
	if !exists {
		return nil, fmt.Errorf("agent %q is not registered", input.AgentID)
	}
	if input.Finalize != nil || input.SynthesisOnly {
		return r.newRegistryCatalog(registration.Definition), nil
	}
	catalog := r.newRegistryCatalog(registration.Definition)
	catalog.runLabels = cloneLabels(input.RunContext.Labels)
	if registration.Definition.registryTools != nil {
		if err := registration.Definition.registryTools.Resolve(ctx, catalog); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

// newRegistryCatalog owns a copy of compiled model definitions for this
// planning activity. The advertised specifications remain its permission list.
func (r *Runtime) newRegistryCatalog(definition AgentDefinition) *RegistryCatalog {
	r.mu.RLock()
	definitions := maps.Clone(r.toolDefinitions)
	r.mu.RUnlock()
	return &RegistryCatalog{
		runtime: r, sources: definition.registryTools, definition: definition,
		specs:       maps.Clone(definition.specByName),
		definitions: definitions,
		selections:  make(map[tools.Ident]registrySelection),
		labels:      make(map[tools.Ident][]string),
	}
}

// spec includes the static correction tools that existing recovery contracts
// may add explicitly to the advertised list. This lookup does not grant access.
func (c *RegistryCatalog) spec(name tools.Ident) (tools.ToolSpec, bool) {
	if spec, ok := c.specs[name]; ok {
		return spec, true
	}
	return c.runtime.toolSpec(name)
}

// RunLabels returns a copy of the trusted labels for this planning activity.
// Applications may use them to select product-specific registry sources; model
// arguments do not provide these labels or authorize catalog membership.
func (c *RegistryCatalog) RunLabels() map[string]string {
	return cloneLabels(c.runLabels)
}
