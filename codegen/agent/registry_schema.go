// Package codegen prepares complete registry declarations from the evaluated tool
// contract. Templates emit literal schemas, metadata, and search terms; provider
// startup does not inspect ToolSpec or reconstruct any static declaration.
package codegen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa/v3/codegen"
)

type (
	// registrySchemaFileData records the literal declarations and the local
	// string values needed by optional fields in Goa's generated wire types.
	registrySchemaFileData struct {
		Schemas []*genregistry.ToolSchema
		Strings []registryStringLocal
	}

	registryStringLocal struct {
		Name  string
		Value string
	}
)

// prepareRegistrySchema builds the one declaration used by both generated
// registration data and its fingerprint. Agent and control declarations retain
// their kind so dynamic consumers reject unsupported execution semantics.
func prepareRegistrySchema(tool *ToolData, entry *toolEntry) error {
	kind := "service"
	if tool.Toolset.AgentToolAgentID != "" {
		kind = "agent"
	} else if entry.Bookkeeping || entry.TerminalRun || entry.ReplanOnTimeout {
		kind = "control"
	}
	contract := &genregistry.ConsumerContract{
		Kind:  kind,
		Title: entry.Title,
		Search: &genregistry.ToolSearchDocument{
			Length: entry.Search.Length,
			Terms:  entry.Search.Terms,
		},
		Payload:        registryTypeMetadata(entry.Payload),
		Result:         registryTypeMetadata(entry.Result),
		RequiredLabels: requiredLabels([]*ToolData{tool}),
		ResultReminder: registryOptionalString(entry.ResultReminder),
	}
	if len(entry.Meta) > 0 {
		contract.Meta = entry.Meta
	}
	if entry.Bounds != nil {
		contract.Bounds = &genregistry.ToolBounds{}
		if paging := entry.Bounds.Paging; paging != nil {
			contract.Bounds.Paging = &genregistry.ToolPaging{
				ContinueTool:    registryOptionalString(paging.ContinueTool),
				SourceTool:      registryOptionalString(paging.SourceTool),
				ReplayPayload:   paging.ReplayPayload,
				CursorField:     paging.CursorField,
				NextCursorField: paging.NextCursorField,
			}
		}
	}
	if confirmation := entry.Confirmation; confirmation != nil {
		contract.Confirmation = &genregistry.ToolConfirmation{
			Title:                registryOptionalString(confirmation.Title),
			PromptTemplate:       confirmation.PromptTemplate,
			DeniedResultTemplate: confirmation.DeniedResultTemplate,
		}
	}
	for _, item := range entry.ServerData {
		contract.ServerData = append(contract.ServerData, &genregistry.ToolServerData{
			Kind:        item.Kind,
			Audience:    item.Audience,
			Description: registryOptionalString(item.Description),
			Schema:      item.Type.SchemaJSON,
			Type:        registryTypeMetadata(item.Type),
		})
	}
	encoded, err := json.Marshal(contract)
	if err != nil {
		return fmt.Errorf("encode tool %q consumer contract: %w", entry.Name, err)
	}
	resultSchema := []byte(`{"type":"null"}`)
	if entry.Result != nil {
		resultSchema = entry.Result.SchemaJSON
	}
	entry.RegistrySchema = &genregistry.ToolSchema{
		Name:                   entry.Name,
		Description:            &entry.Description,
		Tags:                   entry.Tags,
		PayloadSchema:          entry.Payload.SchemaJSON,
		ExecutionPayloadSchema: entry.Payload.ExecutionSchemaJSON,
		ResultSchema:           resultSchema,
		ConsumerContract:       contract,
	}
	entry.ConsumerContractJSON = encoded
	return nil
}

// registryTypeMetadata copies the generator's schema variants and structured
// field records. It never parses JSON Schema to recover those facts.
func registryTypeMetadata(data *typeData) *genregistry.ToolTypeMetadata {
	if data == nil {
		return nil
	}
	result := &genregistry.ToolTypeMetadata{
		Name:                     registryOptionalString(data.TypeName),
		SchemaWithoutRootExample: data.SchemaWithoutRootExampleJSON,
	}
	if len(data.ExampleJSON) > 0 {
		result.ExampleJSON = data.ExampleJSON
	}
	for _, field := range data.Fields {
		record := &genregistry.ToolFieldMetadata{
			Path:        registryFieldPath(field.Path),
			JSONType:    registryOptionalString(field.JSONType),
			Description: registryOptionalString(field.Description),
		}
		if len(field.DiscriminatorValues) > 0 {
			record.DiscriminatorValues = field.DiscriminatorValues
		}
		for _, branch := range field.Branches {
			record.Branches = append(record.Branches, &genregistry.ToolUnionBranch{
				Discriminator: registryFieldPath(branch.Discriminator),
				Value:         branch.Value,
			})
		}
		result.Fields = append(result.Fields, record)
	}
	return result
}

// registryFieldPath retains the distinction between fixed property names and
// collection entries through the generated wire union.
func registryFieldPath(path []fieldPathSegmentData) []*genregistry.ToolFieldPathSegment {
	if len(path) == 0 {
		return nil
	}
	result := make([]*genregistry.ToolFieldPathSegment, 0, len(path))
	for _, segment := range path {
		var value genregistry.ToolFieldSegment
		if segment.Dynamic {
			value = genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})
		} else {
			value = genregistry.NewToolFieldSegmentField(genregistry.ToolFieldSegmentBranchField(segment.Name))
		}
		result = append(result, &genregistry.ToolFieldPathSegment{Segment: value})
	}
	return result
}

// registryOptionalString gives absent optional wire fields one representation.
// Required fields are emitted directly by their owning declaration.
func registryOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// toolRegistrySchemasFile records optional string locals before rendering the
// literal tree. Reflection runs only inside the generator; generated programs
// contain no reflection, conversion loops, or branches over static metadata.
func toolRegistrySchemasFile(ts *ToolsetData, entries []*toolEntry) *codegen.File {
	data := &registrySchemaFileData{}
	for _, entry := range entries {
		data.Schemas = append(data.Schemas, entry.RegistrySchema)
	}
	references := make(map[*string]string)
	collectRegistryStringLocals(reflect.ValueOf(data.Schemas), data, references)
	return &codegen.File{
		Path: filepath.Join(ts.SpecsDir, "registry_schemas.go"),
		SectionTemplates: []*codegen.SectionTemplate{
			codegen.Header(ts.Name+" registry declarations", ts.SpecsPackageName, []*codegen.ImportSpec{
				codegen.NewImport("genregistry", "goa.design/goa-ai/registry/gen/registry"),
			}),
			{
				Name:   "tool-registry-schemas",
				Source: agentsTemplates.Read(toolRegistrySchemasFileT),
				Data:   data,
				FuncMap: map[string]any{
					"pointer": func(value *string) (string, error) {
						name, ok := references[value]
						if !ok {
							return "", fmt.Errorf("registry declaration has an unplanned string pointer")
						}
						return "&" + name, nil
					},
					"segment": registryFieldSegmentLiteral,
				},
			},
		},
	}
}

// collectRegistryStringLocals assigns a fresh local to every optional string.
// Map values in the contract are strings or integers and need no local address.
func collectRegistryStringLocals(value reflect.Value, data *registrySchemaFileData, references map[*string]string) {
	// Scalar values and maps of scalars never require an addressable local.
	//nolint:exhaustive
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return
		}
		if text, ok := value.Interface().(*string); ok {
			name := fmt.Sprintf("registryText%d", len(data.Strings)+1)
			data.Strings = append(data.Strings, registryStringLocal{Name: name, Value: *text})
			references[text] = name
			return
		}
		collectRegistryStringLocals(value.Elem(), data, references)
	case reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			collectRegistryStringLocals(value.Index(index), data, references)
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).IsExported() {
				collectRegistryStringLocals(value.Field(index), data, references)
			}
		}
	default:
		// All other fields are value literals and need no addressable local.
	}
}

// registryFieldSegmentLiteral selects the exact generated union constructor
// before code is emitted, including fixed empty or punctuation-containing names.
func registryFieldSegmentLiteral(segment genregistry.ToolFieldSegment) (string, error) {
	if field, ok := segment.AsField(); ok {
		return fmt.Sprintf("genregistry.NewToolFieldSegmentField(%q)", field), nil
	}
	if element, ok := segment.AsElement(); ok && element != nil {
		return "genregistry.NewToolFieldSegmentElement(&genregistry.ToolCollectionElement{})", nil
	}
	return "", fmt.Errorf("registry declaration has an invalid field path segment")
}
