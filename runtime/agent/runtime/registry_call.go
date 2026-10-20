// Package runtime reads a selected registry contract without consulting the latest
// catalog. Execution, confirmation, and saved-result validation therefore use
// the same definition even after the provider replaces its registration.
package runtime

import (
	"fmt"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// noStaticTools is the lookup for a dynamic call whose consuming Agent is
	// not registered here; it owns no static contracts or declarations.
	noStaticTools struct{}
)

// registryCallMetadata reads policy facts from the exact selected declaration.
// Historical names alone cannot borrow another agent's static policy metadata.
// lookup supplies generated declarations for calls to compiled tools.
func registryCallMetadata(call ToolCall, lookup toolSpecLookup) (policy.ToolMetadata, error) {
	resolved, err := registrycontract.Read(call.Registry, lookup.registryDeclaration)
	if err != nil {
		return policy.ToolMetadata{}, err
	}
	declaration, err := resolved.Declaration(call.Name)
	if err != nil {
		return policy.ToolMetadata{}, err
	}
	spec := resolved.Specs[call.Name]
	title := declaration.ConsumerContract.Title

	return policy.ToolMetadata{
		RequiresUI:       spec.RequiresUI || spec.Confirmation != nil,
		TextOnlyContract: spec.TextOnly != nil,
		ID:               call.Name, Title: title,
		Description: spec.Description, Tags: spec.Tags,
		BudgetClass: policy.ToolBudgetClassBudgeted,
	}, nil
}

// lookupCallSpec resolves the contract owned by this call. Static calls keep
// the existing registered lookup; registry calls must contain the named tool
// in their validated saved definition.
func lookupCallSpec(call ToolCall, lookup toolSpecLookup) (tools.ToolSpec, bool, error) {
	if call.Registry == nil {
		spec, ok := lookup.spec(call.Name)
		return spec, ok, nil
	}
	resolved, err := registrycontract.Read(call.Registry, lookup.registryDeclaration)
	if err != nil {
		return tools.ToolSpec{}, false, err
	}
	spec, ok := resolved.Specs[call.Name]
	if !ok {
		return tools.ToolSpec{}, false, fmt.Errorf("saved registry contract contains no tool %q", call.Name)
	}
	return spec, true, nil
}

// resolveRegistryExecution checks execution permission and required run labels
// against the current consuming design and the call's retained definition.
// It never replaces the selected contract with a newer remote registration.
func (r *Runtime) resolveRegistryExecution(agentID agent.Ident, call ToolCall) (*registrycontract.Resolution, error) {
	reg, ok := r.agentByID(agentID)
	if !ok {
		return nil, fmt.Errorf("agent %q does not consume registry tools", agentID)
	}
	resolved, err := validateRegistrySource(reg.Definition, call)
	if err != nil {
		return nil, err
	}
	declaration, err := resolved.Declaration(call.Name)
	if err != nil {
		return nil, err
	}
	for _, label := range declaration.ConsumerContract.RequiredLabels {
		if call.Labels[label] == "" {
			return nil, fmt.Errorf("registry tool %q requires run label %q", call.Name, label)
		}
	}
	return resolved, nil
}

// validateRegistrySource checks a saved selection against the current generated
// consumption rules. A replacement registration cannot change a saved call.
func validateRegistrySource(definition AgentDefinition, call ToolCall) (*registrycontract.Resolution, error) {
	if definition.registryTools == nil {
		return nil, fmt.Errorf("agent %q does not consume registry tools", definition.route.ID)
	}
	resolved, err := registrycontract.Read(call.Registry, definition.registryDeclaration)
	if err != nil {
		return nil, err
	}
	version := ""
	if resolved.Registered.Toolset.Version != nil {
		version = string(*resolved.Registered.Toolset.Version)
	}
	if !definition.registryTools.Allows(call.Registry.Registry, resolved.Registered.Toolset.Name, version) {
		return nil, fmt.Errorf("agent %q does not consume registry %q toolset %q at version %q",
			definition.route.ID, call.Registry.Registry, resolved.Registered.Toolset.Name, version)
	}
	if spec, exists := definition.specByName[call.Name]; exists {
		if err := validateStaticRegistrySpec(spec, resolved); err != nil {
			return nil, err
		}
		if err := validateStaticAgentRoute(definition, spec, resolved); err != nil {
			return nil, err
		}
	}
	if _, exists := resolved.Specs[call.Name]; !exists {
		return nil, fmt.Errorf("saved registry contract contains no tool %q", call.Name)
	}
	return resolved, nil
}

// recordedToolCalls indexes completed calls by execution ID. The existing
// planner outputs retain their selected definitions; no second history of
// registrations is needed to decode result events.
func recordedToolCalls(outputs []*planner.ToolOutput) (map[string]ToolCall, error) {
	calls := make(map[string]ToolCall, len(outputs))
	for _, output := range outputs {
		if output == nil || output.ToolCallID == "" {
			return nil, fmt.Errorf("saved tool output requires a call ID")
		}
		if _, exists := calls[output.ToolCallID]; exists {
			return nil, fmt.Errorf("duplicate saved tool output for call %q", output.ToolCallID)
		}
		calls[output.ToolCallID] = ToolCall{
			Name: output.Name, ToolCallID: output.ToolCallID,
			Payload: output.Payload, Registry: output.Registry,
		}
	}
	return calls, nil
}

// recordedResultCall correlates a result with its exact saved call. Compiled
// bookkeeping tools do not produce planner outputs and need only their name.
func recordedResultCall(name tools.Ident, id string, calls map[string]ToolCall) (ToolCall, error) {
	call, ok := calls[id]
	if !ok {
		return ToolCall{Name: name, ToolCallID: id}, nil
	}
	if call.Name != name {
		return ToolCall{}, fmt.Errorf("result for call %q names %q instead of %q", id, name, call.Name)
	}
	return call, nil
}

// callSpecLookup restricts bound-call decoding to the consuming Agent's static
// tools. A static name in another Agent never changes a dynamic call's codec.
func (r *Runtime) callSpecLookup(call ToolCall) toolSpecLookup {
	if call.Registry == nil {
		return r
	}
	executor, isAgent := call.Subject.Executor.AsAgent()
	if isAgent {
		if registration, exists := r.agentByID(agent.Ident(executor)); exists {
			return registration.Definition
		}
	}
	return noStaticTools{}
}

// agentTools returns the static contracts and declarations of one consuming
// agent registered on this worker, or none when it is not registered here.
func (r *Runtime) agentTools(agentID agent.Ident) toolSpecLookup {
	if registration, exists := r.agentByID(agentID); exists {
		return registration.Definition
	}
	return noStaticTools{}
}

// spec reports that a dynamic call has no static contract.
func (noStaticTools) spec(tools.Ident) (tools.ToolSpec, bool) {
	return tools.ToolSpec{}, false
}

// registryDeclaration reports that a dynamic call has no generated declaration.
func (noStaticTools) registryDeclaration(tools.Ident) (*genregistry.ToolSchema, bool) {
	return nil, false
}

// isDynamicRegistryCall selects remote decoding only when this consuming Agent
// does not own the named generated tool. Saved source validation remains required.
func (r *Runtime) isDynamicRegistryCall(call ToolCall) bool {
	if call.Registry == nil {
		return false
	}
	_, static := r.callSpecLookup(call).spec(call.Name)
	return !static
}

// callSpec uses the call's saved consuming Agent to choose generated or portable
// codecs without allowing unrelated registrations to change its result types.
func (r *Runtime) callSpec(call ToolCall) (tools.ToolSpec, bool, error) {
	return lookupOwnedCallSpec(call, r.callSpecLookup(call))
}

// lookupOwnedCallSpec accepts only a lookup scoped to the consuming definition.
// Bound static tools retain generated codecs after their saved declaration is checked.
func lookupOwnedCallSpec(call ToolCall, lookup toolSpecLookup) (tools.ToolSpec, bool, error) {
	if call.Registry != nil {
		if spec, exists := lookup.spec(call.Name); exists {
			resolved, err := registrycontract.Read(call.Registry, lookup.registryDeclaration)
			if err != nil {
				return tools.ToolSpec{}, false, err
			}
			if err := validateStaticRegistrySpec(spec, resolved); err != nil {
				return tools.ToolSpec{}, false, err
			}
			return spec, true, nil
		}
	}
	return lookupCallSpec(call, lookup)
}
