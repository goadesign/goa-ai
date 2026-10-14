// Package runtime routes registry Agent calls to the selected provider worker.
// That worker loads the exact saved configuration and prepares the child input.
// Workflow replay reuses the recorded input without loading provider code on
// the consumer or resolving a newer configuration.
package runtime

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// AgentToolConfiguration contains history, labels and policy prepared by a
	// provider for one accepted operation. Runtime publishes the history and
	// exact control values before execution consumes them.
	AgentToolConfiguration struct {
		// Metadata retains provider-owned fixed facts used by the planner.
		Metadata map[string]any
		// History explicitly selects literal messages or one completed source run.
		History AgentHistory
		// RenderedPrompts identifies the prompt versions used in new messages.
		RenderedPrompts []prompt.RenderEvent
		// Labels supplies the child configuration's execution labels.
		Labels map[string]string
		// Policy supplies the child configuration's per-run execution policy.
		Policy *PolicyOverrides
	}

	// AgentToolResolver receives the saved original invocation and prepares
	// history and planner metadata from the validated call. Name identifies the
	// original provider operation; TranscriptName retains the consumer name. The
	// reference comes from the selected registry declaration, never model input.
	AgentToolResolver func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error)
)

// RegisterAgentToolResolver installs configuration loading on the worker that
// runs the Agent. Registration must finish before Seal or the first run; registry
// consumers do not register this resolver.
func (r *Runtime) RegisterAgentToolResolver(executor agent.Ident, resolver AgentToolResolver) error {
	if executor == "" || resolver == nil {
		return errors.New("runtime: Agent executor and configuration resolver are required")
	}
	r.registrationMu.Lock()
	defer r.registrationMu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registrationClosed.Load() {
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

// selectedAgentToolConfig uses the static child definition or the retained
// registry route after checking the parent's declared registry access. Model
// arguments cannot supply a worker route.
func (r *Runtime) selectedAgentToolConfig(call ToolCall) (*AgentToolConfig, error) {
	if call.Registry == nil {
		return r.agentToolConfig(call.Name)
	}
	executor, isAgent := call.Subject.Executor.AsAgent()
	if !isAgent {
		return nil, errors.New("child Agent tool requires an Agent parent subject")
	}
	parent, ok := r.agentByID(agent.Ident(executor))
	if !ok {
		return nil, fmt.Errorf("parent agent %q is not registered", executor)
	}
	child, err := childDefinitionForCall(call, parent.Definition)
	if err != nil {
		return nil, err
	}
	return &AgentToolConfig{Definition: child}, nil
}

// prepareRegistryAgentChild runs on the provider worker, validates the selected
// operation input and renders the exact configuration named by its registration.
func (r *Runtime) prepareRegistryAgentChild(ctx context.Context, origin genregistry.ExecutionInvocation, call ToolCall) (*AgentToolConfiguration, error) {
	cfg, err := r.providerAgentToolConfig(call)
	if err != nil {
		return nil, err
	}
	declaration, err := registryAgentDeclaration(call)
	if err != nil {
		return nil, err
	}
	spec, err := providerOperationSpec(cfg.Definition, declaration)
	if err != nil {
		return nil, err
	}
	if _, err := spec.ExecutionPayloadCodec.FromJSON(call.Payload); err != nil {
		return nil, fmt.Errorf("agent tool %q payload: %w", call.Name, err)
	}
	// A published name selects the saved caller contract. The provider receives
	// its original operation on this local copy, while the caller keeps its alias
	// for catalog lookup, transcript entries, and child run identity.
	if call.Name != spec.Name {
		call.ModelName = call.TranscriptName()
		call.Name = spec.Name
	}
	return r.resolveAgentConfiguration(ctx, origin, cfg.Definition, declaration.ConsumerContract.Agent.Configuration, &call)
}

// resolveAgentConfiguration loads the selected immutable configuration on its
// provider, then returns owned messages, labels and policy for this operation.
func (r *Runtime) resolveAgentConfiguration(ctx context.Context, origin genregistry.ExecutionInvocation, definition AgentDefinition, reference string, call *ToolCall) (*AgentToolConfiguration, error) {
	r.mu.RLock()
	resolver := r.agentToolResolvers[definition.route.ID]
	r.mu.RUnlock()
	if resolver == nil {
		return nil, fmt.Errorf("agent executor %q has no configuration resolver", definition.route.ID)
	}
	ownedCall := cloneToolCall(*call)
	ownedOrigin := origin
	if root, ok := origin.Invocation.AsRoot(); ok {
		value := *root
		ownedOrigin.Invocation = genregistry.NewExecutionInvocationKindRoot(&value)
	} else if tool, ok := origin.Invocation.AsTool(); ok {
		value := *tool
		ownedOrigin.Invocation = genregistry.NewExecutionInvocationKindTool(&value)
	}
	config, err := resolver(ctx, ownedOrigin, reference, &ownedCall)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, errors.New("agent configuration resolver returned nil")
	}
	owned := *config
	owned.Metadata = cloneMetadata(config.Metadata)
	owned.History, err = cloneAgentHistory(config.History)
	if err != nil {
		return nil, err
	}
	owned.Labels = mergeLabels(cloneLabels(call.Labels), config.Labels)
	owned.Policy = clonePolicyOverrides(config.Policy)
	if call.TextOnly {
		if owned.Policy == nil {
			owned.Policy = &PolicyOverrides{}
		}
		owned.Policy.TextOnly = true
	}
	owned.RenderedPrompts = clonePromptRenderEvents(config.RenderedPrompts)
	if err := validateRequiredLabels(definition, owned.Labels); err != nil {
		return nil, err
	}
	return &owned, nil
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
	if binding != nil {
		resolved, err := registrycontract.Read(binding)
		if err != nil {
			return nil, err
		}
		declaration, err := resolved.Declaration(name)
		if err != nil {
			return nil, err
		}
		spec, err := compileAgentOperation(declaration, executor)
		if err != nil {
			return nil, err
		}
		return &spec, nil
	}
	spec, exists := lookup(name)
	if !exists {
		return nil, nil
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
