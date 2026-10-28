// Package codegen generates consumer input installers that validate application-owned
// fields before replacing the local model input. Local and published providers
// use separate entry points
// so a publication always carries its owner-selected source identity.
package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
)

type (
	// consumerBindingData contains linked names for one private assembly and its
	// installation methods. No new application value type is generated.
	consumerBindingData struct {
		Local, Published, Prepare, Original, FullCodec, ModelCodec     string
		LookupModelCodec, LookupLocal, LookupPublished, LookupPrepare  string
		UnknownLookupKeyError                                          string
		Runtime, Registry, Tools, RawJSON, Fmt, JSON, Slices       string
		AgentID                                                    string
		Model, TextOnly, Context, LookupModel, LookupTextOnly      *typeData
		Fields                                                     []consumerBindingField
		Lookup                                                     *consumerBindingField
		HasTextOnly                                                bool
	}
	// consumerBindingField describes one parameter using its provider field type.
	consumerBindingField struct {
		Name, JSONName, Parameter, Type, Validate string
		Pointer                                   bool
	}
	// consumerToolsetFieldBindingData contains linked names for toolset-level
	// Bind<Field>Input and Bind<Field>Lookup helpers.
	consumerToolsetFieldBindingData struct {
		FieldName                     string
		Local, Published              string
		LookupLocal, LookupPublished  string
		Parameter, Type               string
		Runtime, Registry             string
		ToolLocals, ToolPublished     []string
		LookupLocals, LookupPublisheds []string
	}
	// consumerLookupErrorHelperData contains package qualifiers for the lookup
	// validation error constructor.
	consumerLookupErrorHelperData struct {
		Name, Fmt, Slices, Strings, Tools string
	}
)

// consumerBindingFile links field references from the provider's Go layouts and
// emits exact installation methods without runtime field-name interpretation.
func consumerBindingFile(plan *consumerInputPlan) (*codegen.File, error) {
	pkg := plan.packages.public
	sections := []*codegen.SectionTemplate{codegen.Header("Consumer input installation", "inputbinding", plan.packages.fileImports.publicSpecs.Imports())}
	validators, err := plan.validationSections()
	if err != nil {
		return nil, err
	}
	sections = append(sections, validators...)
	if plan.unknownLookupKeyError != nil {
		sections = append(sections, &codegen.SectionTemplate{
			Name:   "consumer-input-lookup-error",
			Source: consumerLookupErrorHelperSource,
			Data: &consumerLookupErrorHelperData{
				Name:    plan.unknownLookupKeyError.Name(),
				Fmt:     pkg.ImportName("fmt"),
				Slices:  pkg.ImportName("slices"),
				Strings: pkg.ImportName("strings"),
				Tools:   pkg.ImportName("goa.design/goa-ai/runtime/agent/tools"),
			},
		})
	}
	for _, tool := range plan.tools {
		provider := plan.provider.tools[tool.expression.Name]
		providerAlias := pkg.ImportName(plan.provider.public.ImportPath())
		data := &consumerBindingData{
			ModelCodec: tool.modelCodec.Name(), Local: tool.local.Name(), Published: tool.published.Name(), Prepare: tool.prepare.Name(),
			Original:  providerAlias + "." + provider.spec.Name(),
			FullCodec: providerAlias + "." + provider.payloadType.exportedCodec.Name(),
			Runtime:   pkg.ImportName("goa.design/goa-ai/runtime/agent/runtime"),
			Registry:  pkg.ImportName("goa.design/goa-ai/registry/gen/registry"),
			Tools:     pkg.ImportName("goa.design/goa-ai/runtime/agent/tools"),
			RawJSON:   pkg.ImportName("goa.design/goa-ai/runtime/agent/rawjson"),
			Fmt:       pkg.ImportName("fmt"),
			Model:     tool.model, TextOnly: tool.textOnly, Context: tool.context,
			HasTextOnly: !tool.expression.RequiresUI && tool.expression.Confirmation == nil,
		}
		if plan.reference.SourceExport != nil {
			data.AgentID = plan.reference.SourceExport.Agent.ID
		}
		for index, layout := range tool.fieldLayouts {
			data.Fields = append(data.Fields, consumerBindingField{
				Validate:  tool.fieldValidators[index].name.Name(),
				Name:      layout.FieldName(true),
				JSONName:  modelJSONName(tool.fields[index]),
				Parameter: "bound" + layout.FieldName(true),
				Type:      layout.Link(pkg.ImportPath(), pkg.ImportName).Ref(),
				Pointer:   tool.fieldPointers[index],
			})
		}
		if tool.lookupModel != nil {
			data.LookupModelCodec = tool.lookupModelCodec.Name()
			data.LookupLocal = tool.lookupLocal.Name()
			data.LookupPublished = tool.lookupPublished.Name()
			data.LookupPrepare = tool.lookupPrepare.Name()
			data.UnknownLookupKeyError = plan.unknownLookupKeyError.Name()
			data.LookupModel = tool.lookupModel
			data.LookupTextOnly = tool.lookupTextOnly
			data.Lookup = &data.Fields[0]
			data.JSON = pkg.ImportName("encoding/json")
			data.Slices = pkg.ImportName("slices")
		}
		sections = append(sections, &codegen.SectionTemplate{Name: "consumer-input-binding-" + tool.expression.Name, Source: consumerBindingSource, Data: data})
	}
	for _, group := range plan.fieldBindings {
		if group.local == nil {
			continue
		}
		firstLayout := group.tools[0].fieldLayouts[0]
		groupData := &consumerToolsetFieldBindingData{
			FieldName: group.goName,
			Local:     group.local.Name(),
			Published: group.published.Name(),
			Parameter: "bound" + firstLayout.FieldName(true),
			Type:      firstLayout.Link(pkg.ImportPath(), pkg.ImportName).Ref(),
			Runtime:   pkg.ImportName("goa.design/goa-ai/runtime/agent/runtime"),
			Registry:  pkg.ImportName("goa.design/goa-ai/registry/gen/registry"),
		}
		for _, tool := range group.tools {
			groupData.ToolLocals = append(groupData.ToolLocals, tool.local.Name())
			groupData.ToolPublished = append(groupData.ToolPublished, tool.published.Name())
		}
		if group.hasLookup {
			groupData.LookupLocal = group.lookupLocal.Name()
			groupData.LookupPublished = group.lookupPublish.Name()
			for _, tool := range group.tools {
				groupData.LookupLocals = append(groupData.LookupLocals, tool.lookupLocal.Name())
				groupData.LookupPublisheds = append(groupData.LookupPublisheds, tool.lookupPublished.Name())
			}
		}
		sections = append(sections, &codegen.SectionTemplate{
			Name:   "consumer-field-binding-" + group.field,
			Source: consumerToolsetFieldBindingSource,
			Data:   groupData,
		})
	}
	return &codegen.File{Path: filepath.Join(plan.dir, "binding.go"), SectionTemplates: sections}, nil
}

const consumerLookupErrorHelperSource = `
// {{ .Name }} builds a canonical enum validation error when the model supplies a
// lookup key that does not exist in the application-supplied lookup table.
func {{ .Name }}(field, value string, allowed []string) error {
	msg := {{ .Fmt }}.Sprintf("field %s: unknown lookup key %q", field, value)
	if len(allowed) > 0 {
		msg = {{ .Fmt }}.Sprintf("field %s: unknown lookup key %q (allowed: %s)", field, value, {{ .Strings }}.Join(allowed, ", "))
	}
	return {{ .Tools }}.NewValidationError(
		msg,
		[]*{{ .Tools }}.FieldIssue{{ "{" }}{
			Field:      field,
			Constraint: "invalid_enum_value",
			Allowed:    {{ .Slices }}.Clone(allowed),
		}{{ "}" }},
		nil,
	)
}
`

const consumerBindingSource = `
// {{ .ModelCodec }} encodes the provider payload using this consumer's model
// fields and decodes model JSON while rejecting application-supplied fields.
func {{ .ModelCodec }}() {{ .Tools }}.JSONCodec[{{ if .Model.Pointer }}*{{ end }}{{ .Model.FullRef }}] {
    return {{ .Tools }}.JSONCodec[{{ if .Model.Pointer }}*{{ end }}{{ .Model.FullRef }}]{
        ToJSON: {{ .Model.MarshalFunc }},
        FromJSON: {{ .Model.UnmarshalFunc }},
    }
}

// {{ .Local }} validates and copies the supplied fields, then installs this
// consumer's model input on the compiled local tool for one planning activity.
func {{ .Local }}(catalog *{{ .Runtime }}.RegistryCatalog{{ range .Fields }}, {{ .Parameter }} {{ .Type }}{{ end }}) error {
    original := {{ .Original }}()
    {{- if .AgentID }}
    // This consumer selects an exported Agent, so retain its child execution route.
    original.IsAgentTool = true
    original.AgentID = {{ printf "%q" .AgentID }}
    {{- end }}
    bound, err := {{ .Prepare }}(original{{ range .Fields }}, {{ .Parameter }}{{ end }})
    if err != nil { return err }
    return catalog.BindInput(original, bound)
}

// {{ .Published }} validates and copies the supplied fields, then installs this
// consumer's model input only on publications of the exact selected source.
func {{ .Published }}(catalog *{{ .Runtime }}.RegistryCatalog, registry string, source *{{ .Registry }}.CatalogSource{{ range .Fields }}, {{ .Parameter }} {{ .Type }}{{ end }}) error {
    original := {{ .Original }}()
    bound, err := {{ .Prepare }}(original{{ range .Fields }}, {{ .Parameter }}{{ end }})
    if err != nil { return err }
    return catalog.BindSourceInput(registry, source, original, bound)
}

// {{ .Prepare }} captures a private validated copy of application-owned fields.
// Each prepared call copies its decoded payload before inserting those values,
// then returns the provider's complete JSON input without changing model input.
func {{ .Prepare }}(original {{ .Tools }}.ToolSpec{{ range .Fields }}, {{ .Parameter }} {{ .Type }}{{ end }}) ({{ .Tools }}.ToolSpec, error) {
    {{- range .Fields }}
    if err := {{ .Validate }}({{ .Parameter }}); err != nil { return {{ $.Tools }}.ToolSpec{}, {{ $.Fmt }}.Errorf("bound field {{ .Name }}: %w",err) }
    {{- end }}
    values := &{{ .Model.FullRef }}{
        {{- range .Fields }}
        {{ .Name }}: {{ if .Pointer }}&{{ end }}{{ .Parameter }},
        {{- end }}
    }
    encoded, err := {{ .Context.MarshalFunc }}(values)
    if err != nil { return {{ .Tools }}.ToolSpec{}, {{ .Fmt }}.Errorf("encode bound input: %w", err) }
    captured, err := {{ .Context.UnmarshalFunc }}(encoded)
    if err != nil { return {{ .Tools }}.ToolSpec{}, {{ .Fmt }}.Errorf("validate bound input: %w", err) }
    bound := original
    bound.Payload = {{ .Tools }}.TypeSpec{
        Name: {{ printf "%q" .Model.TypeName }},
        Schema: {{ .Tools }}.RawJSON({{ printf "%q" .Model.SchemaJSON }}),
        SchemaWithoutRootExample: {{ .Tools }}.RawJSON({{ printf "%q" .Model.SchemaWithoutRootExampleJSON }}),
        Fields: {{ .Tools }}.CloneFieldMetadata({{ .Model.FieldsVar }}),
        Codec: {{ .Model.GenericCodec }},
    }
    {{- if .HasTextOnly }}
    textOnly := *original.TextOnly
    textOnly.Payload = {{ .Tools }}.TypeSpec{
        Name: {{ printf "%q" .TextOnly.TypeName }},
        Schema: {{ .Tools }}.RawJSON({{ printf "%q" .TextOnly.SchemaJSON }}),
        SchemaWithoutRootExample: {{ .Tools }}.RawJSON({{ printf "%q" .TextOnly.SchemaWithoutRootExampleJSON }}),
        Fields: {{ .Tools }}.CloneFieldMetadata({{ .TextOnly.FieldsVar }}),
        Codec: {{ .TextOnly.GenericCodec }},
    }
    bound.TextOnly = &textOnly
    {{- end }}
    bound.PrepareInput = func(decoded any) ({{ .RawJSON }}.Message, error) {
        payload, ok := decoded.(*{{ .Model.FullRef }})
        if !ok || payload == nil {
            return nil, {{ .Fmt }}.Errorf("decoded input for %q has type %T", original.Name, decoded)
        }
        complete := *payload
        {{- range .Fields }}
        complete.{{ .Name }} = captured.{{ .Name }}
        {{- end }}
        result, err := {{ .FullCodec }}().ToJSON(&complete)
        if err != nil { return nil, {{ .Fmt }}.Errorf("encode complete input: %w", err) }
        return {{ .RawJSON }}.Message(result), nil
    }
    return bound, nil
}
{{- if .Lookup }}

// {{ .LookupModelCodec }} encodes and decodes the provider payload using this
// consumer's string-keyed lookup model fields before lookup resolution.
func {{ .LookupModelCodec }}() {{ .Tools }}.JSONCodec[{{ if .LookupModel.Pointer }}*{{ end }}{{ .LookupModel.FullRef }}] {
    return {{ .Tools }}.JSONCodec[{{ if .LookupModel.Pointer }}*{{ end }}{{ .LookupModel.FullRef }}]{
        ToJSON: {{ .LookupModel.MarshalFunc }},
        FromJSON: {{ .LookupModel.UnmarshalFunc }},
    }
}

// {{ .LookupLocal }} validates and copies the supplied lookup table, then
// installs this consumer's string-keyed lookup model input on the compiled
// local tool for one planning activity.
func {{ .LookupLocal }}(catalog *{{ .Runtime }}.RegistryCatalog, table map[string]{{ .Lookup.Type }}) error {
    original := {{ .Original }}()
    {{- if .AgentID }}
    // This consumer selects an exported Agent, so retain its child execution route.
    original.IsAgentTool = true
    original.AgentID = {{ printf "%q" .AgentID }}
    {{- end }}
    bound, err := {{ .LookupPrepare }}(original, table)
    if err != nil { return err }
    return catalog.BindInput(original, bound)
}

// {{ .LookupPublished }} validates and copies the supplied lookup table, then
// installs this consumer's string-keyed lookup model input only on publications
// of the exact selected source.
func {{ .LookupPublished }}(catalog *{{ .Runtime }}.RegistryCatalog, registry string, source *{{ .Registry }}.CatalogSource, table map[string]{{ .Lookup.Type }}) error {
    original := {{ .Original }}()
    bound, err := {{ .LookupPrepare }}(original, table)
    if err != nil { return err }
    return catalog.BindSourceInput(registry, source, original, bound)
}

// {{ .LookupPrepare }} captures a private validated copy of the lookup table.
// Each decoded call validates the string lookup key against the table, replaces
// the field with the resolved provider value, and encodes the complete JSON input.
func {{ .LookupPrepare }}(original {{ .Tools }}.ToolSpec, table map[string]{{ .Lookup.Type }}) ({{ .Tools }}.ToolSpec, error) {
    captured := make(map[string]*{{ .Model.FullRef }}, len(table))
    allowedKeys := make([]string, 0, len(table))
    for key, value := range table {
        if key == "" {
            return {{ .Tools }}.ToolSpec{}, {{ .Fmt }}.Errorf("bound lookup field {{ .Lookup.Name }}: empty lookup key")
        }
        if err := {{ .Lookup.Validate }}(value); err != nil {
            return {{ .Tools }}.ToolSpec{}, {{ .Fmt }}.Errorf("bound lookup field {{ .Lookup.Name }}[%q]: %w", key, err)
        }
        values := &{{ .Model.FullRef }}{
            {{ .Lookup.Name }}: {{ if .Lookup.Pointer }}&{{ end }}value,
        }
        encoded, err := {{ .Context.MarshalFunc }}(values)
        if err != nil { return {{ .Tools }}.ToolSpec{}, {{ .Fmt }}.Errorf("encode bound lookup input %q: %w", key, err) }
        decoded, err := {{ .Context.UnmarshalFunc }}(encoded)
        if err != nil { return {{ .Tools }}.ToolSpec{}, {{ .Fmt }}.Errorf("validate bound lookup input %q: %w", key, err) }
        captured[key] = decoded
        allowedKeys = append(allowedKeys, key)
    }
    {{ .Slices }}.Sort(allowedKeys)
    decodeWithLookup := func(data []byte, unmarshal func([]byte) ({{ if .Model.Pointer }}*{{ end }}{{ .Model.FullRef }}, error)) (any, error) {
        payload, err := unmarshal(data)
        if err != nil {
            return nil, err
        }
        var key struct {
            Value *string ` + "`" + `json:{{ printf "%q" .Lookup.JSONName }}` + "`" + `
        }
        if err := {{ .JSON }}.Unmarshal(data, &key); err != nil {
            return nil, err
        }
        if key.Value != nil {
            resolved, ok := captured[*key.Value]
            if !ok {
                return nil, {{ .UnknownLookupKeyError }}({{ printf "%q" .Lookup.JSONName }}, *key.Value, allowedKeys)
            }
            payload.{{ .Lookup.Name }} = resolved.{{ .Lookup.Name }}
        }
        return payload, nil
    }
    bound := original
    lookupCodec := {{ .LookupModel.GenericCodec }}
    lookupCodec.FromJSON = func(data []byte) (any, error) {
        return decodeWithLookup(data, {{ .LookupModel.UnmarshalFunc }})
    }
    bound.Payload = {{ .Tools }}.TypeSpec{
        Name: {{ printf "%q" .LookupModel.TypeName }},
        Schema: {{ .Tools }}.RawJSON({{ printf "%q" .LookupModel.SchemaJSON }}),
        SchemaWithoutRootExample: {{ .Tools }}.RawJSON({{ printf "%q" .LookupModel.SchemaWithoutRootExampleJSON }}),
        Fields: {{ .Tools }}.CloneFieldMetadata({{ .LookupModel.FieldsVar }}),
        Codec: lookupCodec,
    }
    {{- if .HasTextOnly }}
    textOnly := *original.TextOnly
    lookupTextOnlyCodec := {{ .LookupTextOnly.GenericCodec }}
    lookupTextOnlyCodec.FromJSON = func(data []byte) (any, error) {
        return decodeWithLookup(data, {{ .LookupTextOnly.UnmarshalFunc }})
    }
    textOnly.Payload = {{ .Tools }}.TypeSpec{
        Name: {{ printf "%q" .LookupTextOnly.TypeName }},
        Schema: {{ .Tools }}.RawJSON({{ printf "%q" .LookupTextOnly.SchemaJSON }}),
        SchemaWithoutRootExample: {{ .Tools }}.RawJSON({{ printf "%q" .LookupTextOnly.SchemaWithoutRootExampleJSON }}),
        Fields: {{ .Tools }}.CloneFieldMetadata({{ .LookupTextOnly.FieldsVar }}),
        Codec: lookupTextOnlyCodec,
    }
    bound.TextOnly = &textOnly
    {{- end }}
    bound.PrepareInput = func(decoded any) ({{ .RawJSON }}.Message, error) {
        payload, ok := decoded.(*{{ .Model.FullRef }})
        if !ok || payload == nil {
            return nil, {{ .Fmt }}.Errorf("decoded input for %q has type %T", original.Name, decoded)
        }
        result, err := {{ .FullCodec }}().ToJSON(payload)
        if err != nil { return nil, {{ .Fmt }}.Errorf("encode complete input: %w", err) }
        return {{ .RawJSON }}.Message(result), nil
    }
    return bound, nil
}
{{- end }}
`

const consumerToolsetFieldBindingSource = `
// {{ .Local }} binds {{ .FieldName }} across every tool in this toolset that
// declares it, hiding the field from the model input schema.
func {{ .Local }}(catalog *{{ .Runtime }}.RegistryCatalog, {{ .Parameter }} {{ .Type }}) error {
    {{- range .ToolLocals }}
    if err := {{ . }}(catalog, {{ $.Parameter }}); err != nil { return err }
    {{- end }}
    return nil
}

// {{ .Published }} binds {{ .FieldName }} across every tool in this toolset
// published from the selected source.
func {{ .Published }}(catalog *{{ .Runtime }}.RegistryCatalog, registry string, source *{{ .Registry }}.CatalogSource, {{ .Parameter }} {{ .Type }}) error {
    {{- range .ToolPublished }}
    if err := {{ . }}(catalog, registry, source, {{ $.Parameter }}); err != nil { return err }
    {{- end }}
    return nil
}
{{- if .LookupLocal }}

// {{ .LookupLocal }} installs a string-keyed lookup table for {{ .FieldName }}
// across every tool in this toolset that declares it.
func {{ .LookupLocal }}(catalog *{{ .Runtime }}.RegistryCatalog, table map[string]{{ .Type }}) error {
    {{- range .LookupLocals }}
    if err := {{ . }}(catalog, table); err != nil { return err }
    {{- end }}
    return nil
}

// {{ .LookupPublished }} installs a string-keyed lookup table for {{ .FieldName }}
// across every tool in this toolset published from the selected source.
func {{ .LookupPublished }}(catalog *{{ .Runtime }}.RegistryCatalog, registry string, source *{{ .Registry }}.CatalogSource, table map[string]{{ .Type }}) error {
    {{- range .LookupPublisheds }}
    if err := {{ . }}(catalog, registry, source, table); err != nil { return err }
    {{- end }}
    return nil
}
{{- end }}
`
