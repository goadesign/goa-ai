// Package codegen generates typed evaluation suites and application scaffolds.
// This file renders suite, input, contract, and application-owned templates.
package codegen

import (
	"goa.design/goa/v3/codegen"
)

// suiteSections renders one self-contained generated eval package file.
func suiteSections(data *suiteData) []*codegen.SectionTemplate {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "fmt"},
		{Path: "time"},
		{Path: "goa.design/goa-ai/eval", Name: "eval"},
	}
	if data.NeedsUTF8 {
		imports = append(imports, &codegen.ImportSpec{Path: "unicode/utf8"})
	}
	if data.NeedsGoa {
		imports = append(imports, codegen.GoaImport(""))
	}
	imports = append(imports, data.Imports...)
	return []*codegen.SectionTemplate{
		codegen.Header(data.Name+" evaluation suite", data.Package, imports),
		{
			Name:    "evaluation-suite",
			Source:  suiteTemplate,
			Data:    data,
			FuncMap: codegen.TemplateFuncs(),
		},
	}
}

// contractSections renders a precomputed lookup over the attached agent's
// reachable generated specs packages.
func contractSections(packageName string, data *contractData) []*codegen.SectionTemplate {
	imports := make([]*codegen.ImportSpec, 0, 2+len(data.Specs))
	imports = append(imports,
		&codegen.ImportSpec{Path: "fmt"},
		&codegen.ImportSpec{Path: "goa.design/goa-ai/runtime/agent/tools", Name: "tools"},
	)
	for _, specs := range data.Specs {
		imports = append(imports, &codegen.ImportSpec{Name: specs.Alias, Path: specs.Path})
	}
	return []*codegen.SectionTemplate{
		codegen.Header(data.AgentID+" reachable tool contracts", packageName, imports),
		{
			Name:   "evaluation-tool-contracts",
			Source: contractTemplate,
			Data:   data,
		},
	}
}

const suiteTemplate = `type (
{{- range .Types }}
	{{ comment .Description }}
	{{ .Name }} {{ .Def }}

{{- end }}
	// Hooks is implemented by the application running this evaluation suite.
	// Methods may run concurrently and each owns its complete system interaction.
	Hooks interface {
{{- range .Scenarios }}
		// {{ .Method }} executes the {{ .RawID }} scenario.
		{{ .Method }}(context.Context{{ if .HasInput }}, {{ .InputRef }}{{ end }}) (eval.Result, error)
{{- end }}
	}

	// Inputs supplies the application-owned value for every typed scenario.
	Inputs struct {
{{- range .Scenarios }}
	{{- if .HasInput }}
		// {{ .InputField }} is passed to the {{ .RawID }} hook.
		{{ .InputField }} {{ .InputRef }}
	{{- end }}
{{- end }}
	}
)

// New validates application inputs and binds hooks to the immutable generated
// suite definition.
func New(hooks Hooks, inputs Inputs) (eval.Suite, error) {
	if hooks == nil {
		return eval.Suite{}, fmt.Errorf("evaluation hooks are required")
	}
{{- range .Scenarios }}
	{{- if .InputValidator }}
	if err := {{ .InputValidator }}(inputs.{{ .InputField }}); err != nil {
		return eval.Suite{}, fmt.Errorf("validate {{ .RawID }} input: %w", err)
	}
	{{- end }}
{{- end }}
	return eval.Suite{
		ID:          {{ .ID }},
		Description: {{ .Description }},
		Scenarios: []eval.Scenario{
{{- range .Scenarios }}
			{
				ID:          {{ .ID }},
				Description: {{ .Description }},
				Tags:        []string{ {{- range .Tags }}{{ . }}, {{- end }}},
				Timeout:     time.Duration({{ .Timeout }}),
				Run: func(ctx context.Context) (eval.Result, error) {
					return hooks.{{ .Method }}(ctx{{ if .HasInput }}, inputs.{{ .InputField }}{{ end }})
				},
			},
{{- end }}
		},
	}, nil
}

{{- range .Validators }}
// {{ .Name }} validates a generated evaluation input.
func {{ .Name }}(value {{ .Ref }}) (err error) {
{{- if .Pointer }}
	if value == nil {
		return goa.MissingFieldError("input", "evaluation scenario")
	}
{{- end }}
{{- range .Lines }}
	{{ . }}
{{- end }}
	return
}

{{- end }}`

const contractTemplate = `// MustToolContract returns the generated production
// contract for a tool reachable from {{ .AgentID }}. An unknown identifier is
// an evaluation programming error.
func MustToolContract(name tools.Ident) *tools.ToolSpec {
{{- range .Specs }}
	if spec, ok := {{ .Alias }}.Spec(name); ok {
		return spec
	}
{{- end }}
	panic(fmt.Sprintf("tool %q is not statically reachable from {{ .AgentID }}", name))
}
`
