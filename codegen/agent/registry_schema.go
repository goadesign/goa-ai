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
	// registrySchemaFileData plans small literal constructors. Each generated
	// method owns only its optional strings, keeping compiler work bounded by
	// one tool or field instead of the whole registry catalog.
	registrySchemaFileData struct {
		Name        string
		Description string
		Tags        []string
		Schemas     []*registryLiteralFunction
		Functions   []*registryLiteralFunction
		methods     map[any]string
	}

	registryLiteralFunction struct {
		Name       string
		Kind       string
		Value      any
		Strings    []registryStringLocal
		references map[*string]string
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

// toolRegistrySchemasFile emits literal constructors for tools, type metadata,
// and individual fields. Smaller functions keep large catalogs practical to
// compile without parsing schemas or interpreting metadata at provider startup.
func toolRegistrySchemasFile(ts *ToolsetData, entries []*toolEntry) *codegen.File {
	owner := ts.definition.Owner.Ref
	data := &registrySchemaFileData{
		Name: owner.QualifiedName, Description: owner.Description, Tags: owner.Tags,
		methods: make(map[any]string),
	}
	for _, entry := range entries {
		data.Schemas = append(data.Schemas, data.add("schema", entry.RegistrySchema))
	}
	sections := make([]*codegen.SectionTemplate, 0, 2+len(data.Functions))
	sections = append(sections,
		codegen.Header(ts.Name+" registry declarations", ts.SpecsPackageName, []*codegen.ImportSpec{
			codegen.NewImport("genregistry", "goa.design/goa-ai/registry/gen/registry"),
		}),
		&codegen.SectionTemplate{
			Name: "tool-registry-schemas",
			Source: `// registryDeclarations groups private generated constructors. It holds no state.
 type registryDeclarations struct{}

// Toolset returns the authored toolset declaration with fresh owned schemas.
// The registry supplies RegisteredAt when accepting the declaration.
func Toolset() *genregistry.Toolset {
{{- if .Description }}
    description := {{ printf "%q" .Description }}
{{- end }}
    return &genregistry.Toolset{
        Name: {{ printf "%q" .Name }},
{{- if .Description }}
        Description: &description,
{{- end }}
{{- if .Tags }}
        Tags: []string{ {{ range .Tags }}{{ printf "%q" . }},{{ end }} },
{{- end }}
        Tools: ToolSchemas(),
    }
}

// ToolSchemas returns complete generated declarations with fresh owned values.
func ToolSchemas() []*genregistry.ToolSchema {
{{- if .Schemas }}
    declarations := registryDeclarations{}
{{- end }}
    return []*genregistry.ToolSchema{
{{- range .Schemas }}
        declarations.{{ .Name }}(),
{{- end }}
    }
}`,
			Data: data,
		},
	)
	for _, function := range data.Functions {
		sections = append(sections, &codegen.SectionTemplate{
			Name:   "registry-" + function.Name,
			Source: agentsTemplates.Read(toolRegistrySchemasFileT),
			Data:   function,
			FuncMap: map[string]any{
				"pointer": func(value *string) (string, error) {
					name, ok := function.references[value]
					if !ok {
						return "", fmt.Errorf("registry declaration has an unplanned string pointer")
					}
					return "&" + name, nil
				},
				"reference": func(value any) (string, error) {
					name, ok := data.methods[value]
					if !ok {
						return "", fmt.Errorf("registry declaration has an unplanned constructor")
					}
					return "declarations." + name + "()", nil
				},
				"segment": registryFieldSegmentLiteral,
			},
		})
	}
	return &codegen.File{Path: filepath.Join(ts.SpecsDir, "registry_schemas.go"), SectionTemplates: sections}
}

// add reserves a method before visiting its children. The method calls below it
// are fixed during generation; they do not dispatch on arriving runtime data.
func (d *registrySchemaFileData) add(kind string, value any) *registryLiteralFunction {
	function := &registryLiteralFunction{
		Name: fmt.Sprintf("%s%d", kind, len(d.Functions)+1), Kind: kind, Value: value,
		references: make(map[*string]string),
	}
	d.Functions = append(d.Functions, function)
	d.methods[value] = function.Name
	d.collect(reflect.ValueOf(value).Elem(), function)
	return function
}

// collect assigns strings to their owning constructor and emits separate
// constructors for nested type and field metadata. Reflection ends at codegen.
func (d *registrySchemaFileData) collect(value reflect.Value, function *registryLiteralFunction) {
	// Scalar values and maps of scalars need no addressable local.
	//nolint:exhaustive
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return
		}
		switch item := value.Interface().(type) {
		case *string:
			name := fmt.Sprintf("registryText%d", len(function.Strings)+1)
			function.Strings = append(function.Strings, registryStringLocal{Name: name, Value: *item})
			function.references[item] = name
			return
		case *genregistry.ToolTypeMetadata:
			d.add("metadata", item)
			return
		case *genregistry.ToolFieldMetadata:
			d.add("field", item)
			return
		}
		d.collect(value.Elem(), function)
	case reflect.Slice:
		for index := 0; index < value.Len(); index++ {
			d.collect(value.Index(index), function)
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if value.Type().Field(index).IsExported() {
				d.collect(value.Field(index), function)
			}
		}
	default:
		// Other fields are value literals and require no local variable.
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
