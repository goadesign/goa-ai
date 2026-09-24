// Package runtime executes registry Agent tools using preconfigured workers
// and immutable application configurations. Application resolution runs in the
// existing child preparation activity, whose recorded output is reused during
// workflow replay.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// AgentToolConfiguration contains the messages, labels, and policy prepared
	// for one child call. The runtime publishes its messages before returning
	// the compact child activity result to workflow history.
	AgentToolConfiguration struct {
		// Messages is the exact initial transcript supplied by the resolver.
		Messages []*model.Message
		// RenderedPrompts identifies the prompt versions used in Messages.
		RenderedPrompts []prompt.RenderEvent
		// Labels supplies the child configuration's execution labels.
		Labels map[string]string
		// Policy supplies the child configuration's per-run execution policy.
		Policy *PolicyOverrides
	}

	// AgentToolResolver loads an immutable application configuration and prepares
	// a child transcript from the validated tool call. The reference comes from
	// the selected registry declaration, never from model-authored arguments.
	AgentToolResolver func(context.Context, string, *ToolCall) (*AgentToolConfiguration, error)
)

// WithAgentExecutors authorizes preconfigured child workers for registry Agent
// tools. It returns a new definition and does not read the registry or advertise
// tools. Callers and workers must use the same composed definition.
func (d AgentDefinition) WithAgentExecutors(executors ...*AgentDefinition) AgentDefinition {
	d.agents = maps.Clone(d.agents)
	for _, executor := range executors {
		if executor == nil || !executor.valid() {
			panic("runtime: Agent executor requires a complete definition")
		}
		if _, exists := d.agents[executor.route.ID]; exists {
			panic(fmt.Sprintf("runtime: duplicate Agent executor %q", executor.route.ID))
		}
		d.agents[executor.route.ID] = *executor
	}
	d.agents[d.route.ID] = d
	return d
}

// RegisterAgentToolResolver installs application configuration loading for one
// preconfigured executor. Registration must finish before Seal or the first run.
func (r *Runtime) RegisterAgentToolResolver(executor agent.Ident, resolver AgentToolResolver) error {
	if executor == "" || resolver == nil {
		return errors.New("runtime: Agent executor and configuration resolver are required")
	}
	r.registrationMu.Lock()
	defer r.registrationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registrationClosed {
		return ErrRegistrationClosed
	}
	if _, exists := r.agentToolResolvers[executor]; exists {
		return fmt.Errorf("runtime: Agent executor %q already has a resolver", executor)
	}
	if r.agentToolResolvers == nil {
		r.agentToolResolvers = make(map[agent.Ident]AgentToolResolver)
	}
	r.agentToolResolvers[executor] = resolver
	return nil
}

// selectedAgentToolConfig chooses a static registration or the explicitly
// allowed worker from the parent definition. Registry targets cannot select an
// arbitrary workflow name or queue.
func (r *Runtime) selectedAgentToolConfig(call ToolCall) (*AgentToolConfig, error) {
	if call.Registry == nil {
		return r.agentToolConfig(call.Name)
	}
	parent, ok := r.agentByID(call.AgentID)
	if !ok {
		return nil, fmt.Errorf("parent agent %q is not registered", call.AgentID)
	}
	child, err := childDefinitionForCall(call, parent.Definition)
	if err != nil {
		return nil, err
	}
	return &AgentToolConfig{Definition: child}, nil
}

// prepareRegistryAgentChild resolves the saved configuration only after source,
// executor, payload, and required-label checks have accepted the call.
func (r *Runtime) prepareRegistryAgentChild(ctx context.Context, call ToolCall) (*AgentToolConfiguration, error) {
	cfg, err := r.selectedAgentToolConfig(call)
	if err != nil {
		return nil, err
	}
	resolved, err := r.resolveRegistryExecution(call.AgentID, call)
	if err != nil {
		return nil, err
	}
	spec := resolved.Specs[call.Name]
	if _, err := spec.Payload.Codec.FromJSON(call.Payload); err != nil {
		return nil, fmt.Errorf("agent tool %q payload: %w", call.Name, err)
	}
	r.mu.RLock()
	resolver := r.agentToolResolvers[cfg.Definition.route.ID]
	r.mu.RUnlock()
	if resolver == nil {
		return nil, fmt.Errorf("agent executor %q has no configuration resolver", cfg.Definition.route.ID)
	}
	for _, declaration := range resolved.Registered.Toolset.Tools {
		if declaration.Name != call.Name.String() {
			continue
		}
		ownedCall := cloneToolCall(call)
		config, err := resolver(ctx, declaration.ConsumerContract.Agent.Configuration, &ownedCall)
		if err != nil {
			return nil, err
		}
		if config == nil {
			return nil, errors.New("agent configuration resolver returned nil")
		}
		owned := *config
		owned.Messages, err = model.CloneMessages(config.Messages)
		if err != nil {
			return nil, err
		}
		owned.Labels = mergeLabels(cloneLabels(call.Labels), config.Labels)
		owned.Policy = clonePolicyOverrides(config.Policy)
		owned.RenderedPrompts = clonePromptRenderEvents(config.RenderedPrompts)
		if err := validateRequiredLabels(cfg.Definition, owned.Labels); err != nil {
			return nil, err
		}
		return &owned, nil
	}
	return nil, fmt.Errorf("saved registry declaration contains no tool %q", call.Name)
}

// selectedParentTool checks the saved parent contract independently of the
// child's live tools. A newer tool with the same name cannot change this result.
func selectedParentTool(name tools.Ident, binding *tools.RegistryBinding, executor agent.Ident, lookup toolSpecLookup) (*tools.ToolSpec, error) {
	if name == "" {
		if binding != nil {
			return nil, errors.New("parent registry declaration requires a parent tool")
		}
		return nil, nil
	}
	spec, exists, err := lookupCallSpec(ToolCall{Name: name, Registry: binding}, lookup)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	if binding != nil && (!spec.IsAgentTool || spec.AgentID != string(executor)) {
		return nil, fmt.Errorf("parent tool %q does not target Agent executor %q", name, executor)
	}
	return &spec, nil
}

// cloneParentTool gives planner code its own mutable description and schemas.
// Runtime validation retains the original contract for the accepted call.
func cloneParentTool(spec *tools.ToolSpec) *tools.ToolSpec {
	if spec == nil {
		return nil
	}
	cloned := cloneToolSpec(*spec)
	return &cloned
}
