// Package codegen generates typed evaluation suites and application scaffolds.
// This file writes generated suite, input, tool, and starter command files.
package codegen

import (
	"goa.design/goa/v3/codegen"
)

// suiteSections writes one complete generated eval package file.
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

// contractSections writes the tool descriptions available to the suite's agent.
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
	// {{ .Hooks }} is implemented by the application running this evaluation suite.
	// Methods can run at the same time. Each method completes one scenario.
	{{ .Hooks }} interface {
{{- range .Scenarios }}
		// {{ .Method }} executes the {{ .RawID }} scenario.
		{{ .Method }}(context.Context{{ if .HasInput }}, {{ .InputRef }}{{ end }}) (eval.Result, error)
{{- end }}
	}

	// {{ .Inputs }} contains the application value for every typed scenario.
	{{ .Inputs }} struct {
{{- range .Scenarios }}
	{{- if .HasInput }}
		// {{ .InputField }} is passed to the {{ .RawID }} hook.
		{{ .InputField }} {{ .InputRef }}
	{{- end }}
{{- end }}
	}
)

// {{ .New }} validates application inputs and builds the evaluation suite.
func {{ .New }}(hooks {{ .Hooks }}, inputs {{ .Inputs }}) (eval.Suite, error) {
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
func {{ .Name }}(value {{ .Ref }}) error {
{{- if .Pointer }}
	if value == nil {
		return goa.MissingFieldError("input", "evaluation scenario")
	}
{{- end }}
	{{- if .Lines }}
	return {{ .NestedName }}(value, "input")
	{{- else }}
	return nil
	{{- end }}
}

{{- if .Lines }}
// {{ .NestedName }} validates value and starts error field names at path.
func {{ .NestedName }}(value {{ .Ref }}, path string) (err error) {
{{- range .Lines }}
	{{ . }}
{{- end }}
	return
}
{{- end }}

{{- end }}`

const contractTemplate = `// {{ .MustToolContract }} returns the generated description
// for a tool available to {{ .AgentID }}. An unknown name is a programming error.
func {{ .MustToolContract }}(name tools.Ident) *tools.ToolSpec {
{{- range .Specs }}
	if spec, ok := {{ .Alias }}.Spec(name); ok {
		return &spec
	}
{{- end }}
	panic(fmt.Sprintf("tool %q is not statically reachable from {{ .AgentID }}", name))
}
`
