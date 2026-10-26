// Package runtime routes registry Agent calls using their retained operation
// contracts and provider worker routes.
// Consumers use these facts without loading the provider's private definition.
// The provider checks the route against its own registered worker before it
// prepares messages or accepts a continuation.
package runtime

import (
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
)

// registryAgentDeclaration reads the retained registration and requires the
// named operation to target an Agent. A same-name replacement cannot change it.
// lookup supplies generated declarations for calls to compiled tools.
func registryAgentDeclaration(call ToolCall, lookup toolSpecLookup) (*genregistry.ToolSchema, error) {
	resolved, err := registrycontract.Read(call.Registry, lookup.registryDeclaration)
	if err != nil {
		return nil, err
	}
	declaration, err := resolved.Declaration(call.Name)
	if err != nil {
		return nil, err
	}
	if !resolved.Specs[call.Name].IsAgentTool {
		return nil, fmt.Errorf("registry tool %q is not an Agent operation", call.Name)
	}
	return declaration, nil
}

// registeredChildDefinition checks the parent's declared registry access and
// returns only the admitted route. The provider owns its private tool catalog.
func registeredChildDefinition(call ToolCall, parent AgentDefinition) (AgentDefinition, error) {
	resolved, err := validateRegistrySource(parent, call)
	if err != nil {
		return AgentDefinition{}, err
	}
	declaration, err := resolved.Declaration(call.Name)
	if err != nil {
		return AgentDefinition{}, err
	}
	if !resolved.Specs[call.Name].IsAgentTool {
		return AgentDefinition{}, fmt.Errorf("registry tool %q is not an Agent operation", call.Name)
	}
	target := declaration.ConsumerContract.Agent
	return NewAgentDefinition(AgentRoute{
		ID: agent.Ident(target.Executor), WorkflowName: target.Workflow,
		DefaultTaskQueue: target.Queue,
	}, nil, nil, nil, nil, nil, nil), nil
}

// providerAgentToolConfig checks that a retained registry operation selects
// this worker's exact route. Only that worker's resolver can prepare its input.
func (r *Runtime) providerAgentToolConfig(call ToolCall) (*AgentToolConfig, error) {
	cfg, _, err := r.providerAgentOperation(call)
	return cfg, err
}

// providerAgentOperation resolves both the provider worker's registered
// definition and the retained registry declaration in a single contract read.
func (r *Runtime) providerAgentOperation(call ToolCall) (*AgentToolConfig, *genregistry.ToolSchema, error) {
	declaration, err := registryAgentDeclaration(call, r)
	if err != nil {
		return nil, nil, err
	}
	target := declaration.ConsumerContract.Agent
	registration, exists := r.agentByID(agent.Ident(target.Executor))
	if !exists {
		return nil, nil, fmt.Errorf("agent provider %q is not registered on this worker", target.Executor)
	}
	route := registration.Definition.route
	if route.WorkflowName != target.Workflow || route.DefaultTaskQueue != target.Queue {
		return nil, nil, fmt.Errorf("agent operation %q does not match this provider's registered route", call.Name)
	}
	return &AgentToolConfig{Definition: registration.Definition}, declaration, nil
}

// agentChildPreparationConfig uses local registration for a static call and
// this provider's definition for a registry call received on its worker queue.
func (r *Runtime) agentChildPreparationConfig(call ToolCall) (*AgentToolConfig, error) {
	if call.Registry == nil {
		return r.agentToolConfig(call.Name)
	}
	return r.providerAgentToolConfig(call)
}
