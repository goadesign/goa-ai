// Package runtime binds generated native tools to selected provider registrations.
// Generated definitions keep their names, codecs and child routes; each accepted
// registration supplies its immutable provider configuration and admission token.
package runtime

import (
	"context"
	"fmt"
	"slices"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

const staticAgentKind = "agent"

// BindStaticAgent attaches a selected native provider registration to its complete
// generated export set. Expected declarations must be unbound native operations;
// the selected provider supplies configuration while generated children own routes.
// Generated names, codecs and input preparation remain owned by the consumer.
// Before a new page uses this registration, readPrevious receives the saved
// private toolset route, exact token and Agent target, in that order. It must
// read declarations proving that they belong to this same logical resource.
// Accepted calls keep their original target and never call readPrevious.
// The reader returns the owner's exact ResolvedToolset so saved fingerprints use
// the old declarations. When the saved target identifies a different logical
// resource, the reader returns nil without an error. The runtime then offers no
// next page for that entire query chain, while preserving its saved history.
// Malformed targets and failures reading a matching resource remain errors.
func (c *RegistryCatalog) BindStaticAgent(registry string, expected []*genregistry.ToolSchema, selected *genregistry.ResolvedToolset, readPrevious func(context.Context, string, string, *genregistry.AgentToolTarget) (*genregistry.ResolvedToolset, error), names ...tools.Ident) error {
	if readPrevious == nil {
		return fmt.Errorf("static agent binding requires a reader for previous registrations")
	}
	if len(expected) == 0 || len(names) != len(expected) {
		return fmt.Errorf("static agent binding requires the complete generated export set and explicit tool names")
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
	version := ""
	if selected.Toolset.Version != nil {
		version = string(*selected.Toolset.Version)
	}
	if c.sources == nil || !c.sources.Allows(registry, selected.Toolset.Name, version) {
		return fmt.Errorf("registry %q toolset %q is outside this agent's declared consumption", registry, selected.Toolset.Name)
	}
	// Attach only the selected target to each unbound operation. Comparing the
	// complete declarations also checks fields that are not in a consumer ToolSpec.
	declarations := make([]*genregistry.ToolSchema, len(expected))
	seen := make(map[tools.Ident]*genregistry.ToolSchema, len(expected))
	for index, operation := range expected {
		if operation == nil || operation.ConsumerContract == nil || operation.ConsumerContract.Kind != staticAgentKind || operation.ConsumerContract.Agent != nil {
			return fmt.Errorf("static agent binding requires unbound generated agent operations")
		}
		name := tools.Ident(operation.Name)
		if _, exists := seen[name]; exists {
			return fmt.Errorf("static agent binding repeats declaration %q", name)
		}
		seen[name] = operation
		provider, err := resolved.Declaration(name)
		if err != nil {
			return err
		}
		if provider.ConsumerContract.Kind != staticAgentKind {
			return fmt.Errorf("static agent binding requires an agent provider for %q", name)
		}
		declaration := *operation
		contract := *operation.ConsumerContract
		contract.Agent = provider.ConsumerContract.Agent
		declaration.ConsumerContract = &contract
		declarations[index] = &declaration
	}
	// Toolset descriptions and tags belong to registration discovery. Both sides
	// use the selected envelope so only the complete generated operations differ.
	declaration := *selected.Toolset
	declaration.Tools = declarations
	expectedFingerprint, err := toolcontract.Fingerprint(&declaration)
	if err != nil {
		return err
	}
	selectedFingerprint, err := toolcontract.Fingerprint(selected.Toolset)
	if err != nil {
		return err
	}
	if expectedFingerprint != selectedFingerprint {
		return fmt.Errorf("static agent binding differs from the complete generated declarations")
	}
	for index, name := range names {
		if slices.Contains(names[:index], name) {
			return fmt.Errorf("static agent binding repeats tool %q", name)
		}
		if _, exists := seen[name]; !exists {
			return fmt.Errorf("tool %q is not in the generated export set", name)
		}
		if _, exists := c.selections[name]; exists {
			return fmt.Errorf("tool %q already has a selected registration", name)
		}
		spec, exists := c.definition.specByName[name]
		if !exists {
			return fmt.Errorf("tool %q is not static in this consuming definition", name)
		}
		if !spec.IsAgentTool {
			return fmt.Errorf("tool %q is not a generated agent tool", name)
		}
		if err := validateStaticRegistrySpec(spec, resolved); err != nil {
			return err
		}
		if err := validateStaticAgentRoute(c.definition, spec, resolved); err != nil {
			return err
		}
	}
	restored, err := c.restoreCompiledSelections(registry, resolved, names)
	if err != nil {
		return err
	}
	for index, name := range names {
		c.selections[name] = registrySelection{registry: registry, resolution: restored[index], readPreviousAgent: readPrevious}
		c.labels[name] = slices.Clone(seen[name].ConsumerContract.RequiredLabels)
	}
	return nil
}

// validateStaticRegistrySpec checks the saved execution kind and full generated
// policy before callers use the consumer's concrete codecs and input preparation.
func validateStaticRegistrySpec(spec tools.ToolSpec, resolved *registrycontract.Resolution) error {
	if !spec.IsAgentTool {
		return validateStaticServiceSpec(spec, resolved)
	}
	declaration, err := resolved.Declaration(spec.Name)
	if err != nil {
		return err
	}
	if resolved.Source != nil || declaration.ConsumerContract.Kind != staticAgentKind || !equivalentToolSpec(spec, resolved.Specs[spec.Name]) {
		return fmt.Errorf("saved agent tool %q differs from this agent's static contract", spec.Name)
	}
	return nil
}

// validateStaticAgentRoute compares a selected native worker with the consuming
// definition's generated child route. Configuration belongs to the selected
// registration and may differ between revisions without changing this route.
func validateStaticAgentRoute(definition AgentDefinition, spec tools.ToolSpec, resolved *registrycontract.Resolution) error {
	if !spec.IsAgentTool {
		return nil
	}
	declaration, err := resolved.Declaration(spec.Name)
	if err != nil {
		return err
	}
	child, exists := definition.agents[agent.Ident(spec.AgentID)]
	if !exists {
		return fmt.Errorf("child agent %q is not in the current definition graph", spec.AgentID)
	}
	target := declaration.ConsumerContract.Agent
	if target.Executor != string(child.route.ID) || target.Workflow != child.route.WorkflowName || target.Queue != child.route.DefaultTaskQueue {
		return fmt.Errorf("saved agent tool %q differs from its generated child route", spec.Name)
	}
	return nil
}
