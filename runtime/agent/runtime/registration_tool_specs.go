package runtime

// registration_tool_specs.go enforces one immutable global contract per tool
// name before agent or toolset registration reaches the workflow engine. Agent
// aggregate specs and their executable toolset specs may repeat the same
// generated declarative contract, but later registrations never replace the
// first contract or executable toolset owner.

import (
	"bytes"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

type toolSpecRegistration struct {
	specs  []tools.ToolSpec
	lookup ToolMetadataLookup
}

// validateToolSpecRegistrations compares incoming contracts against the
// runtime registry and against earlier entries in the same registration.
func (r *Runtime) validateToolSpecRegistrations(
	registrations ...toolSpecRegistration,
) (map[tools.Ident]compiledToolDefinition, error) {
	r.mu.RLock()
	specs := make(map[tools.Ident]tools.ToolSpec, len(r.toolSpecs))
	for name, spec := range r.toolSpecs {
		specs[name] = spec
	}
	definitions := make(map[tools.Ident]compiledToolDefinition, len(r.toolDefinitions))
	for name, definition := range r.toolDefinitions {
		definitions[name] = definition
	}
	metadata := make(map[tools.Ident]policy.ToolMetadata, len(r.policyToolMetadata))
	for name, meta := range r.policyToolMetadata {
		metadata[name] = meta
	}
	r.mu.RUnlock()

	for _, registration := range registrations {
		for _, spec := range registration.specs {
			if spec.PrepareInput != nil {
				return nil, fmt.Errorf("%w: tool %q input binding belongs to one planning activity, not a shared registration", ErrInvalidConfig, spec.Name)
			}
			if err := r.validateImageSourceProducer(spec); err != nil {
				return nil, err
			}
			if spec.ReplanOnTimeout && !spec.IsAgentTool {
				return nil, fmt.Errorf("%w: tool %q ReplanOnTimeout requires an agent-as-tool registration", ErrInvalidConfig, spec.Name)
			}
			if spec.ExecutionPayloadCodec.FromJSON == nil || spec.ExecutionPayloadCodec.ToJSON == nil {
				return nil, fmt.Errorf("%w: tool %q execution payload codec must define both ToJSON and FromJSON", ErrInvalidConfig, spec.Name)
			}
			if _, err := isComposedContinuationSpec(spec); err != nil {
				return nil, fmt.Errorf("%w: tool %q paging execution: %w", ErrInvalidConfig, spec.Name, err)
			}
			var textOnlyDefinition *model.ToolDefinition
			if spec.TextOnly != nil {
				if spec.TextOnly.Payload.Codec.FromJSON == nil || spec.TextOnly.Payload.Codec.ToJSON == nil || spec.TextOnly.ExecutionCodec.FromJSON == nil || spec.TextOnly.ExecutionCodec.ToJSON == nil {
					return nil, fmt.Errorf("%w: tool %q text-only codecs are required", ErrInvalidConfig, spec.Name)
				}
				if _, err := isComposedContinuationSpec(spec.ForTextOnly()); err != nil {
					return nil, fmt.Errorf("%w: tool %q text-only paging execution: %w", ErrInvalidConfig, spec.Name, err)
				}
				var err error
				textOnlyDefinition, err = model.NewToolDefinitionFromSpec(spec.ForTextOnly())
				if err != nil {
					return nil, fmt.Errorf("%w: tool %q text-only model contract: %w", ErrInvalidConfig, spec.Name, err)
				}
			}
			if err := validateToolResultSpec(spec); err != nil {
				return nil, err
			}
			meta := canonicalToolMetadata(spec, registration.lookup)
			if existing, ok := specs[spec.Name]; ok {
				if !equivalentToolSpec(existing, spec) ||
					!reflect.DeepEqual(metadata[spec.Name], meta) {
					return nil, fmt.Errorf(
						"%w: tool %q is already registered with a different contract",
						ErrInvalidConfig,
						spec.Name,
					)
				}
				if definitions[spec.Name].ordinary == nil {
					return nil, fmt.Errorf(
						"%w: tool %q has no compiled input contract",
						ErrInvalidConfig,
						spec.Name,
					)
				}
				continue
			}
			definition, err := model.NewToolDefinitionFromSpec(spec)
			if err != nil {
				return nil, fmt.Errorf("%w: tool %q input contract: %w", ErrInvalidConfig, spec.Name, err)
			}
			specs[spec.Name] = spec
			definitions[spec.Name] = compiledToolDefinition{ordinary: definition, textOnly: textOnlyDefinition}
			metadata[spec.Name] = meta
		}
	}
	return definitions, nil
}

// mustToolDefinitions compiles runtime-owned specifications whose validity is
// guaranteed by generated code or test construction.
func mustToolDefinitions(specs []tools.ToolSpec) map[tools.Ident]compiledToolDefinition {
	definitions := make(map[tools.Ident]compiledToolDefinition, len(specs))
	for _, spec := range specs {
		definition := compiledToolDefinition{ordinary: model.ToolDefinitionFromSpec(spec)}
		if spec.TextOnly != nil {
			definition.textOnly = model.ToolDefinitionFromSpec(spec.ForTextOnly())
		}
		definitions[spec.Name] = definition
	}
	return definitions
}

// validateToolResultSpec requires either no result declaration or a complete
// codec that can both save and restore the declared result.
func validateToolResultSpec(spec tools.ToolSpec) error {
	hasEncoder := spec.Result.Codec.ToJSON != nil
	hasDecoder := spec.Result.Codec.FromJSON != nil
	if hasEncoder != hasDecoder {
		return fmt.Errorf(
			"%w: tool %q result codec must define both ToJSON and FromJSON",
			ErrInvalidConfig,
			spec.Name,
		)
	}
	if !hasEncoder && !equivalentTypeSpec(spec.Result, tools.TypeSpec{}) {
		return fmt.Errorf(
			"%w: tool %q result metadata requires a complete codec",
			ErrInvalidConfig,
			spec.Name,
		)
	}
	return nil
}

// equivalentToolSpec compares every declarative field without allocating
// normalized copies. Codec functions are implementation values and cannot be
// compared soundly because Go exposes a closure's code address but not its
// captured state. The first registration owns those functions and later
// matching specs never replace it.
func equivalentToolSpec(a, b tools.ToolSpec) bool {
	return a.Name == b.Name &&
		a.Description == b.Description &&
		a.RequiresUI == b.RequiresUI &&
		equivalentModelContract(a.TextOnly, b.TextOnly) &&
		equivalentSearchDocument(a.Search, b.Search) &&
		slices.Equal(a.Tags, b.Tags) &&
		equivalentStringLists(a.Meta, b.Meta) &&
		a.TerminalRun == b.TerminalRun &&
		a.Bookkeeping == b.Bookkeeping &&
		a.ReplanOnTimeout == b.ReplanOnTimeout &&
		a.IsAgentTool == b.IsAgentTool &&
		a.AgentID == b.AgentID &&
		equivalentBoundsSpec(a.Bounds, b.Bounds) &&
		equivalentServerDataSpecs(a.ServerData, b.ServerData) &&
		a.ResultReminder == b.ResultReminder &&
		equivalentConfirmationSpec(a.Confirmation, b.Confirmation) &&
		bytes.Equal(a.ExecutionPayloadSchema, b.ExecutionPayloadSchema) &&
		equivalentTypeSpec(a.Payload, b.Payload) &&
		equivalentTypeSpec(a.Result, b.Result)
}

// equivalentModelContract compares optional text-only model contracts without
// comparing codec closures.
func equivalentModelContract(a, b *tools.ModelContract) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Description == b.Description &&
		equivalentSearchDocument(a.Search, b.Search) &&
		equivalentTypeSpec(a.Payload, b.Payload) &&
		(a.ExecutionSchema == nil) == (b.ExecutionSchema == nil) &&
		bytes.Equal(a.ExecutionSchema, b.ExecutionSchema) &&
		a.ResultReminder == b.ResultReminder
}

// equivalentSearchDocument compares lexical discovery term counts.
func equivalentSearchDocument(a, b tools.SearchDocument) bool {
	if a.Length != b.Length || (a.Terms == nil) != (b.Terms == nil) {
		return false
	}
	return maps.Equal(a.Terms, b.Terms)
}

// equivalentStringLists compares string-list maps while preserving nil vs
// non-nil empty map distinction and treating nil and empty value slices as
// equivalent.
func equivalentStringLists(a, b map[string][]string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return maps.EqualFunc(a, b, slices.Equal)
}

// equivalentBoundsSpec compares optional bounded-result and paging metadata.
func equivalentBoundsSpec(a, b *tools.BoundsSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Paging == nil || b.Paging == nil {
		return a.Paging == b.Paging
	}
	return a.Paging.ContinueTool == b.Paging.ContinueTool &&
		a.Paging.SourceTool == b.Paging.SourceTool &&
		(a.Paging.RetainedFields == nil) == (b.Paging.RetainedFields == nil) &&
		slices.Equal(a.Paging.RetainedFields, b.Paging.RetainedFields) &&
		a.Paging.CursorField == b.Paging.CursorField &&
		a.Paging.NextCursorField == b.Paging.NextCursorField
}

// equivalentServerDataSpecs compares server-data declarations in order.
func equivalentServerDataSpecs(a, b []*tools.ServerDataSpec) bool {
	return slices.EqualFunc(a, b, equivalentServerDataSpec)
}

// equivalentServerDataSpec compares one server-data declaration without
// comparing codec closures.
func equivalentServerDataSpec(a, b *tools.ServerDataSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Kind == b.Kind &&
		a.Audience == b.Audience &&
		a.NativeImage == b.NativeImage &&
		a.Description == b.Description &&
		equivalentTypeSpec(a.Type, b.Type)
}

// equivalentConfirmationSpec compares optional confirmation templates.
func equivalentConfirmationSpec(a, b *tools.ConfirmationSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// equivalentTypeSpec compares one type's schemas and field metadata without
// comparing codec closures. Nil and empty byte/field slices are equivalent.
func equivalentTypeSpec(a, b tools.TypeSpec) bool {
	return a.Name == b.Name &&
		bytes.Equal(a.Schema, b.Schema) &&
		bytes.Equal(a.SchemaWithoutRootExample, b.SchemaWithoutRootExample) &&
		bytes.Equal(a.ExampleJSON, b.ExampleJSON) &&
		slices.EqualFunc(a.Fields, b.Fields, equivalentFieldMetadata)
}

// equivalentFieldMetadata compares one advertised schema field.
func equivalentFieldMetadata(a, b tools.FieldMetadata) bool {
	return slices.Equal(a.Path, b.Path) &&
		a.JSONType == b.JSONType &&
		a.Description == b.Description &&
		slices.EqualFunc(a.Branches, b.Branches, equivalentUnionBranch) &&
		slices.Equal(a.DiscriminatorValues, b.DiscriminatorValues)
}

// equivalentUnionBranch compares one tagged or untagged union branch requirement.
func equivalentUnionBranch(a, b tools.UnionBranch) bool {
	switch left := a.(type) {
	case tools.TaggedUnionBranch:
		right, ok := b.(tools.TaggedUnionBranch)
		return ok && left.Value == right.Value && slices.Equal(left.Discriminator, right.Discriminator)
	case tools.UntaggedUnionBranch:
		right, ok := b.(tools.UntaggedUnionBranch)
		return ok && left.JSONKind == right.JSONKind && left.Index == right.Index && slices.Equal(left.Path, right.Path)
	default:
		return a == b
	}
}

// cloneToolsetRegistration takes ownership of mutable specs, hint maps, and
// nested-agent routing maps while retaining immutable functions and templates.
func cloneToolsetRegistration(reg ToolsetRegistration) ToolsetRegistration {
	reg.Specs = cloneToolSpecs(reg.Specs)
	if reg.ActivityRetryPolicy != nil {
		policy := *reg.ActivityRetryPolicy
		reg.ActivityRetryPolicy = &policy
	}
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
	if spec.TextOnly != nil {
		contract := *spec.TextOnly
		contract.Search.Terms = maps.Clone(contract.Search.Terms)
		contract.Payload = cloneTypeSpec(contract.Payload)
		contract.ExecutionSchema = slices.Clone(contract.ExecutionSchema)
		spec.TextOnly = &contract
	}
	spec.Search.Terms = maps.Clone(spec.Search.Terms)
	spec.Tags = append([]string(nil), spec.Tags...)
	spec.ExecutionPayloadSchema = append(tools.RawJSON(nil), spec.ExecutionPayloadSchema...)
	if spec.Meta != nil {
		meta := spec.Meta
		spec.Meta = make(map[string][]string, len(meta))
		for key, values := range meta {
			spec.Meta[key] = append([]string(nil), values...)
		}
	}
	spec.Payload = cloneTypeSpec(spec.Payload)
	spec.Result = cloneTypeSpec(spec.Result)
	if spec.Bounds != nil {
		bounds := *spec.Bounds
		if bounds.Paging != nil {
			paging := *bounds.Paging
			paging.RetainedFields = slices.Clone(paging.RetainedFields)
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
	spec.Fields = tools.CloneFieldMetadata(spec.Fields)
	return spec
}
