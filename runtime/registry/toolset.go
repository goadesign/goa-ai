// Package registry turns a discovered schema into an immutable toolset.
// Applications load toolsets before constructing their agents. Each returned
// value owns its schemas and codecs, so later catalog changes cannot alter a
// running agent's argument or result contracts.
package registry

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
	registryschema "goa.design/goa-ai/runtime/toolregistry/schema"
)

type (
	// Toolset contains validated tool definitions discovered from a registry.
	// NewToolset copies its input, and accessors return copies of mutable data.
	Toolset struct {
		name    string
		version string
		specs   []tools.ToolSpec
		index   map[tools.Ident]int
	}
)

// NewToolset validates one discovered toolset and compiles its JSON codecs.
// Missing schemas, duplicate names, and unqualified tool identifiers are errors.
// The registry's returned membership is authoritative: a generated registration
// such as alpha.lookup can contain tools named lookup.search.
// The result is fixed for the lifetime of the agent using it.
func NewToolset(schema *ToolsetSchema) (*Toolset, error) {
	if schema == nil || schema.Name == "" {
		return nil, fmt.Errorf("registry toolset name is required")
	}
	if len(schema.Tools) == 0 {
		return nil, fmt.Errorf("registry toolset %q has no tools", schema.Name)
	}
	set := &Toolset{
		name:    schema.Name,
		version: schema.Version,
		specs:   make([]tools.ToolSpec, 0, len(schema.Tools)),
		index:   make(map[tools.Ident]int, len(schema.Tools)),
	}
	for _, tool := range schema.Tools {
		if tool == nil {
			return nil, fmt.Errorf("registry toolset %q contains a nil tool", schema.Name)
		}
		name := tools.Ident(tool.Name)
		if name.Toolset() == "" || name.Tool() == "" {
			return nil, fmt.Errorf("registry tool %q must be a qualified tool identifier", name)
		}
		if _, exists := set.index[name]; exists {
			return nil, fmt.Errorf("registry toolset %q repeats tool %q", schema.Name, name)
		}
		if len(tool.SidecarSchema) != 0 {
			return nil, fmt.Errorf("registry tool %q requires a static contract for server-only result data", name)
		}
		payload, err := registryschema.Codec(tool.PayloadSchema)
		if err != nil {
			return nil, fmt.Errorf("registry tool %q payload: %w", name, err)
		}
		execution, err := registryschema.Codec(tool.ExecutionPayloadSchema)
		if err != nil {
			return nil, fmt.Errorf("registry tool %q execution payload: %w", name, err)
		}
		result, err := registryschema.Codec(tool.ResultSchema)
		if err != nil {
			return nil, fmt.Errorf("registry tool %q result: %w", name, err)
		}
		set.index[name] = len(set.specs)
		set.specs = append(set.specs, tools.ToolSpec{
			Name:                   name,
			Description:            tool.Description,
			Search:                 tools.NewSearchDocument(tool.Name + " " + tool.Description),
			Tags:                   slices.Clone(tool.Tags),
			ExecutionPayloadSchema: slices.Clone(tool.ExecutionPayloadSchema),
			ExecutionPayloadCodec:  execution,
			Payload: tools.TypeSpec{
				Schema: slices.Clone(tool.PayloadSchema),
				Codec:  payload,
			},
			Result: tools.TypeSpec{
				Schema: slices.Clone(tool.ResultSchema),
				Codec:  result,
			},
		})
	}
	slices.SortFunc(set.specs, func(a, b tools.ToolSpec) int {
		return cmp.Compare(a.Name, b.Name)
	})
	for index, spec := range set.specs {
		set.index[spec.Name] = index
	}
	return set, nil
}

// Name identifies the provider toolset whose definitions were loaded.
func (t *Toolset) Name() string {
	return t.name
}

// Version returns the version supplied by the registry for this toolset.
func (t *Toolset) Version() string {
	return t.version
}

// Specs returns independently owned specifications in tool-name order.
func (t *Toolset) Specs() []tools.ToolSpec {
	result := make([]tools.ToolSpec, len(t.specs))
	for index, spec := range t.specs {
		result[index] = cloneDiscoveredSpec(spec)
	}
	return result
}

// Names returns the discovered tool identifiers in sorted order.
func (t *Toolset) Names() []tools.Ident {
	names := make([]tools.Ident, len(t.specs))
	for index, spec := range t.specs {
		names[index] = spec.Name
	}
	return names
}

// Spec returns an independently owned specification for a discovered tool.
func (t *Toolset) Spec(name tools.Ident) (tools.ToolSpec, bool) {
	index, ok := t.index[name]
	if !ok {
		return tools.ToolSpec{}, false
	}
	return cloneDiscoveredSpec(t.specs[index]), true
}

// MetadataByName returns the catalog metadata used for policy filtering.
// Registry schema tools count against the ordinary tool-call budget.
func (t *Toolset) MetadataByName(name tools.Ident) (policy.ToolMetadata, bool) {
	index, ok := t.index[name]
	if !ok {
		return policy.ToolMetadata{}, false
	}
	spec := t.specs[index]
	return policy.ToolMetadata{
		ID:          spec.Name,
		Title:       string(spec.Name),
		Description: spec.Description,
		Tags:        slices.Clone(spec.Tags),
		BudgetClass: policy.ToolBudgetClassBudgeted,
	}, true
}

// ValidatePayload validates a value against the named tool's model arguments.
// An unknown tool is an error rather than an unchecked successful validation.
func (t *Toolset) ValidatePayload(name tools.Ident, value any) error {
	index, ok := t.index[name]
	if !ok {
		return fmt.Errorf("tool %q is not in registry toolset %q", name, t.name)
	}
	_, err := t.specs[index].Payload.Codec.ToJSON(value)
	return err
}

// ValidateResult validates a value against the named tool's result schema.
func (t *Toolset) ValidateResult(name tools.Ident, value any) error {
	index, ok := t.index[name]
	if !ok {
		return fmt.Errorf("tool %q is not in registry toolset %q", name, t.name)
	}
	_, err := t.specs[index].Result.Codec.ToJSON(value)
	return err
}

// cloneDiscoveredSpec copies the mutable fields populated by NewToolset.
// The compiled codec functions capture immutable validators and may be shared.
func cloneDiscoveredSpec(spec tools.ToolSpec) tools.ToolSpec {
	spec.Search.Terms = maps.Clone(spec.Search.Terms)
	spec.Tags = slices.Clone(spec.Tags)
	spec.Payload.Schema = slices.Clone(spec.Payload.Schema)
	spec.ExecutionPayloadSchema = slices.Clone(spec.ExecutionPayloadSchema)
	spec.Result.Schema = slices.Clone(spec.Result.Schema)
	return spec
}
