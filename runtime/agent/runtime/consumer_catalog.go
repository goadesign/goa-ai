// Package runtime keeps consumer input views within one planning activity.
// Static registrations identify local tools; remote bindings name an exact
// registered source so another registry's operation cannot receive bound values.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

// WithInputBinding records the static tools that generated code requires the
// application to bind. Each planning activity calls prepare after source discovery
// and rejects missing installations before the planner can see tools. Finalizers
// also prepare these inputs without reading remote catalogs.
func (d AgentDefinition) WithInputBinding(required []tools.Ident, prepare func(context.Context, *RegistryCatalog) error) AgentDefinition {
	if prepare == nil || d.prepareInputs != nil {
		panic("runtime: input preparation must be supplied exactly once")
	}
	for index, name := range required {
		if _, exists := d.specByName[name]; !exists {
			panic(fmt.Sprintf("runtime: required input binding %q is not a static tool", name))
		}
		if slices.Contains(required[:index], name) {
			panic(fmt.Sprintf("runtime: required input binding %q is repeated", name))
		}
	}
	d.requiredInputBindings = slices.Clone(required)
	d.prepareInputs = prepare
	d.agents = maps.Clone(d.agents)
	d.agents[d.route.ID] = d
	return d
}

// BindInput installs a generated model input view for one statically registered
// tool in this consuming definition. It never selects a remote resource.
func (c *RegistryCatalog) BindInput(original, bound tools.ToolSpec) error {
	declared, exists := c.definition.specByName[original.Name]
	if !exists || !equivalentToolSpec(declared, original) {
		return fmt.Errorf("tool %q input binding does not match this agent's static declaration", original.Name)
	}
	registered, exists := c.runtime.toolSpec(original.Name)
	if !exists || !equivalentToolSpec(registered, original) {
		return fmt.Errorf("tool %q input binding does not match its registered provider", original.Name)
	}
	if _, dynamic := c.selections[original.Name]; dynamic {
		return fmt.Errorf("tool %q input binding requires a static registration", original.Name)
	}
	return c.bindConsumerInput(original.Name, original, bound)
}

// BindSourceInput installs a generated model input view for the derived name of one
// operation from the named registered source. The registry, owner, resource, and
// immutable target must all match; direct imports have no registered source identity.
func (c *RegistryCatalog) BindSourceInput(registry string, source *genregistry.CatalogSource, original, bound tools.ToolSpec) error {
	if registry == "" {
		return fmt.Errorf("input binding requires a registry name")
	}
	if _, err := genregistry.EncodeCatalogSource(source); err != nil {
		return fmt.Errorf("input binding source: %w", err)
	}
	selectedSource := false
	for name, selected := range c.selections {
		if selected.registry != registry || selected.resolution.Source == nil ||
			!sameInputSource(source, selected.resolution.Source) {
			continue
		}
		selectedSource = true
		declaration, err := selected.resolution.Declaration(name)
		if err != nil {
			return err
		}
		if declaration.Name != string(original.Name) {
			continue
		}
		if err := c.bindConsumerInput(name, original, bound); err != nil {
			return err
		}
	}
	if !selectedSource {
		return fmt.Errorf("registry %q input source %q/%q is not selected in this activity", registry, source.Scope, source.Name)
	}
	return nil
}

// bindConsumerInput replaces only an owned model view after checking the full
// provider input contracts. Saved selections and executable codecs stay intact.
func (c *RegistryCatalog) bindConsumerInput(name tools.Ident, original, bound tools.ToolSpec) error {
	if original.Name != bound.Name || bound.PrepareInput == nil ||
		!bytes.Equal(original.ExecutionPayloadSchema, bound.ExecutionPayloadSchema) {
		return fmt.Errorf("tool %q input binding must retain its source name and execution schema", original.Name)
	}
	spec, exists := c.specs[name]
	if !exists {
		return fmt.Errorf("tool %q input binding does not match the selected provider schema", name)
	}
	if spec.PrepareInput != nil {
		return fmt.Errorf("tool %q input is already bound in this planning activity", name)
	}
	if !bytes.Equal(spec.Payload.Schema, original.Payload.Schema) ||
		!bytes.Equal(spec.ExecutionPayloadSchema, original.ExecutionPayloadSchema) {
		return fmt.Errorf("tool %q input binding does not match the selected provider schema", name)
	}
	if (spec.TextOnly == nil) != (original.TextOnly == nil) ||
		spec.TextOnly != nil && (!bytes.Equal(spec.TextOnly.Payload.Schema, original.TextOnly.Payload.Schema) ||
			!bytes.Equal(spec.TextOnly.ExecutionSchema, original.TextOnly.ExecutionSchema)) {
		return fmt.Errorf("tool %q input binding does not match the selected text-only provider schema", name)
	}
	spec = cloneToolSpec(spec)
	spec.Payload = cloneTypeSpec(bound.Payload)
	spec.PrepareInput = bound.PrepareInput
	if spec.TextOnly != nil {
		if bound.TextOnly == nil || !bytes.Equal(spec.TextOnly.ExecutionSchema, bound.TextOnly.ExecutionSchema) {
			return fmt.Errorf("tool %q input binding does not retain the text-only execution contract", name)
		}
		spec.TextOnly.Payload = cloneTypeSpec(bound.TextOnly.Payload)
	}
	definition, err := model.NewToolDefinitionFromSpec(spec)
	if err != nil {
		return fmt.Errorf("tool %q bound model input: %w", name, err)
	}
	definition.Deferred = c.definitions[name].Deferred
	c.specs[name] = spec
	c.definitions[name] = definition
	return nil
}

// sameInputSource compares owner-issued source identity. Native routes are
// immutable revisions; service routes also require their registration token.
func sameInputSource(left, right *genregistry.CatalogSource) bool {
	if left.Scope != right.Scope || left.Name != right.Name || left.Target.Kind() != right.Target.Kind() {
		return false
	}
	if native, ok := left.Target.AsNative(); ok {
		other, _ := right.Target.AsNative()
		return native.Toolset == other.Toolset
	}
	service, _ := left.Target.AsService()
	other, _ := right.Target.AsService()
	return service.Toolset == other.Toolset && service.RegistrationToken == other.RegistrationToken
}
