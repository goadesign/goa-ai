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
	// two installation methods. No new application value type is generated.
	consumerBindingData struct {
		Local, Published, Prepare, Original, FullCodec, ModelCodec string
		Runtime, Registry, Tools, RawJSON, Fmt                     string
		AgentID                                                    string
		Model, TextOnly, Context                                   *typeData
		Fields                                                     []consumerBindingField
		HasTextOnly                                                bool
	}
	// consumerBindingField describes one parameter using its provider field type.
	consumerBindingField struct{ Name, Parameter, Type, Validate string }
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
			data.Fields = append(data.Fields, consumerBindingField{Validate: tool.fieldValidators[index].name.Name(), Name: layout.FieldName(true), Parameter: "bound" + layout.FieldName(true), Type: layout.Link(pkg.ImportPath(), pkg.ImportName).Ref()})
		}
		sections = append(sections, &codegen.SectionTemplate{Name: "consumer-input-binding-" + tool.expression.Name, Source: consumerBindingSource, Data: data})
	}
	return &codegen.File{Path: filepath.Join(plan.dir, "binding.go"), SectionTemplates: sections}, nil
}

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
        {{ .Name }}: {{ .Parameter }},
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
`
