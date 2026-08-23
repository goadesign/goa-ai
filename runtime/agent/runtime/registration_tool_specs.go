package runtime

// registration_tool_specs.go enforces one immutable global contract per tool
// name before agent or toolset registration reaches the workflow engine. Agent
// aggregate specs and their executable toolset specs may repeat the same
// generated declarative contract, but later registrations never replace the
// first contract or executable toolset owner.

import (
	"fmt"
	"maps"
	"reflect"

	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

type toolSpecRegistration struct {
	specs  []tools.ToolSpec
	lookup ToolMetadataLookup
}

// validateToolSpecRegistrations compares incoming contracts against the
// runtime registry and against earlier entries in the same registration.
func (r *Runtime) validateToolSpecRegistrations(registrations ...toolSpecRegistration) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	specs := make(map[tools.Ident]tools.ToolSpec, len(r.toolSpecs))
	for name, spec := range r.toolSpecs {
		specs[name] = spec
	}
	metadata := make(map[tools.Ident]policy.ToolMetadata, len(r.policyToolMetadata))
	for name, meta := range r.policyToolMetadata {
		metadata[name] = meta
	}
	for _, registration := range registrations {
		for _, spec := range registration.specs {
			meta := canonicalToolMetadata(spec, registration.lookup)
			if existing, ok := specs[spec.Name]; ok {
				if !equivalentToolSpec(existing, spec) ||
					!reflect.DeepEqual(metadata[spec.Name], meta) {
					return fmt.Errorf(
						"%w: tool %q is already registered with a different contract",
						ErrInvalidConfig,
						spec.Name,
					)
				}
				continue
			}
			specs[spec.Name] = spec
			metadata[spec.Name] = meta
		}
	}
	return nil
}

// equivalentToolSpec compares every declarative field. Codec functions are
// implementation values and cannot be compared soundly because Go exposes a
// closure's code address but not its captured state. The first registration
// owns those functions and later matching specs never replace it.
func equivalentToolSpec(a, b tools.ToolSpec) bool {
	return reflect.DeepEqual(toolSpecShape(a), toolSpecShape(b))
}

// toolSpecShape removes function values from an owned copy so reflect.DeepEqual
// compares only the generated schema and runtime policy contract.
func toolSpecShape(spec tools.ToolSpec) tools.ToolSpec {
	spec = cloneToolSpec(spec)
	spec.Payload.Codec = tools.JSONCodec[any]{}
	spec.Result.Codec = tools.JSONCodec[any]{}
	spec.CanonicalizeServerData = nil
	for _, item := range spec.ServerData {
		if item == nil {
			continue
		}
		item.Type.Codec = tools.JSONCodec[any]{}
	}
	return spec
}

// cloneAgentRegistration takes ownership of mutable generated contract data.
func cloneAgentRegistration(reg AgentRegistration) AgentRegistration {
	reg.Specs = cloneToolSpecs(reg.Specs)
	reg.RequiredLabels = append([]string(nil), reg.RequiredLabels...)
	return reg
}

// cloneToolsetRegistration takes ownership of mutable specs, hint maps, and
// nested-agent routing maps while retaining immutable functions and templates.
func cloneToolsetRegistration(reg ToolsetRegistration) ToolsetRegistration {
	reg.Specs = cloneToolSpecs(reg.Specs)
	reg.CallHints = maps.Clone(reg.CallHints)
	reg.ResultHints = maps.Clone(reg.ResultHints)
	if reg.AgentTool != nil {
		agentTool := *reg.AgentTool
		agentTool.Templates = maps.Clone(agentTool.Templates)
		agentTool.Texts = maps.Clone(agentTool.Texts)
		agentTool.PromptSpecs = maps.Clone(agentTool.PromptSpecs)
		agentTool.Aliases = maps.Clone(agentTool.Aliases)
		reg.AgentTool = &agentTool
	}
	return reg
}

// cloneToolSpecs returns runtime-owned copies of generated tool contracts.
func cloneToolSpecs(specs []tools.ToolSpec) []tools.ToolSpec {
	cloned := make([]tools.ToolSpec, len(specs))
	for index, spec := range specs {
		cloned[index] = cloneToolSpec(spec)
	}
	return cloned
}

// cloneToolSpec copies every mutable declarative field while retaining codec
// functions, which become owned by the first accepted registration.
func cloneToolSpec(spec tools.ToolSpec) tools.ToolSpec {
	spec.Tags = append([]string(nil), spec.Tags...)
	if spec.Meta != nil {
		spec.Meta = make(map[string][]string, len(spec.Meta))
		for key, values := range spec.Meta {
			spec.Meta[key] = append([]string(nil), values...)
		}
	}
	spec.Payload = cloneTypeSpec(spec.Payload)
	spec.Result = cloneTypeSpec(spec.Result)
	if spec.Bounds != nil {
		bounds := *spec.Bounds
		if bounds.Paging != nil {
			paging := *bounds.Paging
			bounds.Paging = &paging
		}
		spec.Bounds = &bounds
	}
	if spec.Confirmation != nil {
		confirmation := *spec.Confirmation
		spec.Confirmation = &confirmation
	}
	serverData := make([]*tools.ServerDataSpec, len(spec.ServerData))
	for index, item := range spec.ServerData {
		if item == nil {
			continue
		}
		cloned := *item
		cloned.Type = cloneTypeSpec(cloned.Type)
		serverData[index] = &cloned
	}
	spec.ServerData = serverData
	return spec
}

// cloneTypeSpec copies generated raw JSON and field metadata while retaining
// the codec functions owned by the enclosing tool registration.
func cloneTypeSpec(spec tools.TypeSpec) tools.TypeSpec {
	spec.Schema = append(tools.RawJSON(nil), spec.Schema...)
	spec.SchemaWithoutRootExample = append(tools.RawJSON(nil), spec.SchemaWithoutRootExample...)
	spec.ExampleJSON = append(tools.RawJSON(nil), spec.ExampleJSON...)
	spec.FieldDescriptions = maps.Clone(spec.FieldDescriptions)
	spec.FieldJSONTypes = maps.Clone(spec.FieldJSONTypes)
	return spec
}
