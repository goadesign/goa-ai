package runtime

// registration_tool_specs.go enforces one immutable global contract per tool
// name before agent or toolset registration reaches the workflow engine. Agent
// aggregate specs and their executable toolset specs may repeat the same
// generated contract, but a different schema, policy fact, or codec is rejected.

import (
	"fmt"
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

// equivalentToolSpec compares every declarative field and the identity of each
// codec function. Generated aggregate and toolset specs share those functions;
// a different function under the same name is a different executable contract.
func equivalentToolSpec(a, b tools.ToolSpec) bool {
	aShape, aFunctions := toolSpecShape(a)
	bShape, bFunctions := toolSpecShape(b)
	return reflect.DeepEqual(aShape, bShape) &&
		reflect.DeepEqual(aFunctions, bFunctions)
}

// toolSpecShape separates function identities from the data reflect.DeepEqual
// can compare, including codecs nested in server-data declarations.
func toolSpecShape(spec tools.ToolSpec) (tools.ToolSpec, []uintptr) {
	functions := []uintptr{
		functionPointer(spec.Payload.Codec.ToJSON),
		functionPointer(spec.Payload.Codec.FromJSON),
		functionPointer(spec.Result.Codec.ToJSON),
		functionPointer(spec.Result.Codec.FromJSON),
		functionPointer(spec.CanonicalizeServerData),
	}
	spec.Payload.Codec = tools.JSONCodec[any]{}
	spec.Result.Codec = tools.JSONCodec[any]{}
	spec.CanonicalizeServerData = nil
	serverData := make([]*tools.ServerDataSpec, len(spec.ServerData))
	for index, item := range spec.ServerData {
		if item == nil {
			continue
		}
		cloned := *item
		functions = append(
			functions,
			functionPointer(cloned.Type.Codec.ToJSON),
			functionPointer(cloned.Type.Codec.FromJSON),
		)
		cloned.Type.Codec = tools.JSONCodec[any]{}
		serverData[index] = &cloned
	}
	spec.ServerData = serverData
	return spec, functions
}

// functionPointer returns zero for nil functions and the runtime identity for
// a concrete codec function.
func functionPointer(fn any) uintptr {
	value := reflect.ValueOf(fn)
	if !value.IsValid() || value.IsNil() {
		return 0
	}
	return value.Pointer()
}
