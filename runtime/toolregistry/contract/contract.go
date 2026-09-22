// Package contract compiles generated registry declarations into local
// JSON codecs. Generated registry validation checks declaration structure. This package
// checks execution semantics and cross-field invariants;
// it does not reinterpret JSON schemas to recover design metadata.
package contract

import (
	"fmt"
	"maps"
	"slices"

	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
	"goa.design/goa-ai/runtime/toolserverdata"
)

type (
	// serverDataContract keeps each admitted kind's audience and compiled
	// validator independent from mutable specifications returned to callers.
	serverDataContract struct {
		audience string
		codec    tools.JSONCodec[any]
	}

	serverDataContracts map[string]serverDataContract
)

const agentKind = "agent"

// Compile returns an owned tool specification for one portable service or native Agent tool.
// Schema-only declarations and execution kinds requiring compiled integration
// are explicit errors; neither may lose policy metadata during discovery.
func Compile(declaration *genregistry.ToolSchema) (tools.ToolSpec, error) {
	if declaration == nil {
		return tools.ToolSpec{}, fmt.Errorf("tool declaration is required")
	}
	// This generated request validates the shared ToolSchema without requiring
	// provider leases or a registration timestamp. Execution kind is checked below.
	request := genregistryclient.NewProtoRegisterAgentToolsetRequest(&genregistry.AgentToolsetDeclaration{
		Name: declaration.Name, Tools: []*genregistry.ToolSchema{declaration},
	})
	if err := genregistryclient.ValidateRegisterAgentToolsetRequest(request); err != nil {
		return tools.ToolSpec{}, fmt.Errorf("tool %q declaration: %w", declaration.Name, err)
	}
	contract := declaration.ConsumerContract
	if contract == nil {
		return tools.ToolSpec{}, fmt.Errorf("tool %q has no generated consumer contract", declaration.Name)
	}
	if contract.Kind != "service" && contract.Kind != agentKind {
		return tools.ToolSpec{}, fmt.Errorf("tool %q requires compiled %s execution", declaration.Name, contract.Kind)
	}
	if contract.Kind == agentKind && (contract.Agent == nil || contract.Result == nil) {
		return tools.ToolSpec{}, fmt.Errorf("agent tool %q requires an executor, immutable configuration, and result contract", declaration.Name)
	}
	if contract.Kind != agentKind && contract.Agent != nil {
		return tools.ToolSpec{}, fmt.Errorf("tool %q declares an agent target for %s execution", declaration.Name, contract.Kind)
	}
	if len(declaration.SidecarSchema) != 0 {
		return tools.ToolSpec{}, fmt.Errorf("tool %q must declare server data in its consumer contract", declaration.Name)
	}
	search := tools.SearchDocument{Length: contract.Search.Length, Terms: maps.Clone(contract.Search.Terms)}
	if err := search.Validate(); err != nil {
		return tools.ToolSpec{}, fmt.Errorf("tool %q search document: %w", declaration.Name, err)
	}
	payload, err := compileType(declaration.PayloadSchema, contract.Payload)
	if err != nil {
		return tools.ToolSpec{}, fmt.Errorf("tool %q payload: %w", declaration.Name, err)
	}
	executionCodec, err := schema.Codec(declaration.ExecutionPayloadSchema)
	if err != nil {
		return tools.ToolSpec{}, fmt.Errorf("tool %q execution payload: %w", declaration.Name, err)
	}
	spec := tools.ToolSpec{
		Name:                   tools.Ident(declaration.Name),
		Search:                 search,
		Tags:                   slices.Clone(declaration.Tags),
		Payload:                payload,
		ExecutionPayloadSchema: slices.Clone(declaration.ExecutionPayloadSchema),
		ExecutionPayloadCodec:  executionCodec,
	}
	if contract.Agent != nil {
		spec.IsAgentTool = true
		spec.AgentID = contract.Agent.Executor
	}
	if declaration.Description != nil {
		spec.Description = *declaration.Description
	}
	if contract.Meta != nil {
		spec.Meta = make(map[string][]string, len(contract.Meta))
		for key, values := range contract.Meta {
			spec.Meta[key] = slices.Clone(values)
		}
	}
	if contract.Result != nil {
		spec.Result, err = compileType(declaration.ResultSchema, contract.Result)
		if err != nil {
			return tools.ToolSpec{}, fmt.Errorf("tool %q result: %w", declaration.Name, err)
		}
	}
	if contract.Bounds != nil {
		spec.Bounds = &tools.BoundsSpec{}
		if paging := contract.Bounds.Paging; paging != nil {
			spec.Bounds.Paging = &tools.PagingSpec{
				ReplayPayload:   paging.ReplayPayload,
				CursorField:     paging.CursorField,
				NextCursorField: paging.NextCursorField,
			}
			if paging.ContinueTool != nil {
				spec.Bounds.Paging.ContinueTool = tools.Ident(*paging.ContinueTool)
			}
			if paging.SourceTool != nil {
				spec.Bounds.Paging.SourceTool = tools.Ident(*paging.SourceTool)
			}
		}
	}
	if confirmation := contract.Confirmation; confirmation != nil {
		spec.Confirmation = &tools.ConfirmationSpec{
			PromptTemplate:       confirmation.PromptTemplate,
			DeniedResultTemplate: confirmation.DeniedResultTemplate,
		}
		if confirmation.Title != nil {
			spec.Confirmation.Title = *confirmation.Title
		}
	}
	if contract.ResultReminder != nil {
		spec.ResultReminder = *contract.ResultReminder
	}
	if len(contract.ServerData) > 0 {
		contracts := make(serverDataContracts, len(contract.ServerData))
		for _, item := range contract.ServerData {
			if _, exists := contracts[item.Kind]; exists {
				return tools.ToolSpec{}, fmt.Errorf("tool %q repeats server data kind %q", declaration.Name, item.Kind)
			}
			dataType, err := compileType(item.Schema, item.Type)
			if err != nil {
				return tools.ToolSpec{}, fmt.Errorf("tool %q server data %q: %w", declaration.Name, item.Kind, err)
			}
			contracts[item.Kind] = serverDataContract{audience: item.Audience, codec: dataType.Codec}
			specItem := &tools.ServerDataSpec{
				Kind:     item.Kind,
				Audience: tools.ServerDataAudience(item.Audience),
				Type:     dataType,
			}
			if item.Description != nil {
				specItem.Description = *item.Description
			}
			spec.ServerData = append(spec.ServerData, specItem)
		}
		spec.CanonicalizeServerData = contracts.canonicalize
	}
	return spec, nil
}

// compileType uses the supplied schema and the generator's field records
// directly. The JSON codec validates dynamic values without rounding numbers.
func compileType(document []byte, metadata *genregistry.ToolTypeMetadata) (tools.TypeSpec, error) {
	codec, err := schema.Codec(document)
	if err != nil {
		return tools.TypeSpec{}, err
	}
	result := tools.TypeSpec{
		Schema:                   slices.Clone(document),
		SchemaWithoutRootExample: slices.Clone(metadata.SchemaWithoutRootExample),
		ExampleJSON:              slices.Clone(metadata.ExampleJSON),
		Codec:                    codec,
	}
	if metadata.Name != nil {
		result.Name = *metadata.Name
	}
	for _, field := range metadata.Fields {
		path, err := fieldPath(field.Path)
		if err != nil {
			return tools.TypeSpec{}, err
		}
		details := tools.FieldMetadata{
			Path:                path,
			DiscriminatorValues: slices.Clone(field.DiscriminatorValues),
		}
		if field.JSONType != nil {
			details.JSONType = *field.JSONType
		}
		if field.Description != nil {
			details.Description = *field.Description
		}
		for _, branch := range field.Branches {
			discriminator, err := fieldPath(branch.Discriminator)
			if err != nil {
				return tools.TypeSpec{}, err
			}
			details.Branches = append(details.Branches, tools.UnionBranch{
				Discriminator: discriminator, Value: branch.Value,
			})
		}
		result.Fields = append(result.Fields, details)
	}
	return result, nil
}

// fieldPath preserves the generated union distinction rather than splitting
// property names or guessing which strings represent collection entries.
func fieldPath(segments []*genregistry.ToolFieldPathSegment) ([]tools.FieldPathSegment, error) {
	var result []tools.FieldPathSegment
	for _, segment := range segments {
		if value, ok := segment.Segment.AsField(); ok {
			result = append(result, tools.FixedField(value))
		} else if _, ok := segment.Segment.AsElement(); ok {
			result = append(result, tools.DynamicField{})
		} else {
			return nil, fmt.Errorf("tool field path has an unselected segment")
		}
	}
	return result, nil
}

// canonicalize validates the common envelope and then checks every item
// against the kind and audience declared by the resolved tool.
func (contracts serverDataContracts) canonicalize(data rawjson.Message) (rawjson.Message, error) {
	return toolserverdata.Canonicalize(data, contracts.canonicalizeItem)
}

func (contracts serverDataContracts) canonicalizeItem(kind, audience string, data rawjson.Message) (string, rawjson.Message, error) {
	contract, ok := contracts[kind]
	if !ok {
		return "", nil, fmt.Errorf("server data kind %q is not declared by this tool", kind)
	}
	if audience != contract.audience {
		return "", nil, fmt.Errorf("server data kind %q has audience %q; expected %q", kind, audience, contract.audience)
	}
	value, err := contract.codec.FromJSON(data)
	if err != nil {
		return "", nil, err
	}
	encoded, err := contract.codec.ToJSON(value)
	if err != nil {
		return "", nil, err
	}
	return audience, rawjson.Message(encoded), nil
}
