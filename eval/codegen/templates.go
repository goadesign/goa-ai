// Package codegen generates typed evaluation suites and application scaffolds.
// This file writes generated suite, input, tool, and starter command files.
package codegen

import (
	"goa.design/goa/v3/codegen"
)

// suiteSections writes one complete generated eval package file.
func suiteSections(data *suiteData) []*codegen.SectionTemplate {
	return []*codegen.SectionTemplate{
		codegen.Header(data.Name+" evaluation suite", data.Package, data.Imports),
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
	return []*codegen.SectionTemplate{
		codegen.Header(data.AgentID+" reachable tool contracts", packageName, data.Imports),
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
{{- if .HasChecks }}
	// {{ .Checks }} assesses captured values with exact, read-only predicates.
	// An empty diagnostic passes; a nonempty diagnostic explains a failure.
	// Methods must not call the product or reload reference data.
	{{ .Checks }} interface {
{{- range .CheckMethods }}
		// {{ .Method }} checks the saved observation. {{ .Description }}
		{{ .Method }}({{ .Ref }}) string
{{- end }}
	}
{{- end }}
	// {{ .Hooks }} captures typed product evidence. Methods may run concurrently.
	{{ .Hooks }} interface {
{{- if .HasChecks }}
		{{ .Checks }}
{{- end }}
{{- range .Scenarios }}
		// {{ .Method }} runs {{ .RawID }} and returns its observed outcome.
		{{ .Method }}(context.Context{{ if .HasInput }}, {{ .InputRef }}{{ end }}) ({{ .ObservationRef }}, error)
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
	suite, err := {{ .ForAssessment }}({{ if .HasChecks }}hooks{{ end }})
	if err != nil { return eval.Suite{}, err }
{{- range .Scenarios }}
{{- if .HasInput }}
	suite.Scenarios[{{ .Index }}].Input, err = json.Marshal(inputs.{{ .InputField }})
	if err != nil { return eval.Suite{}, fmt.Errorf("encode {{ .RawID }} input: %w", err) }
{{- end }}
	suite.Scenarios[{{ .Index }}].Capture = func(ctx context.Context) (rawjson.Message, error) {
		observed, err := hooks.{{ .Method }}(ctx{{ if .HasInput }}, inputs.{{ .InputField }}{{ end }})
		if err != nil { return nil, err }
		return {{ .Encode }}(observed)
	}
{{- end }}
	return suite, nil
}

// {{ .ForAssessment }} builds an offline suite using saved observations only.
// It requires no capture hooks, live inputs, or product clients.
func {{ .ForAssessment }}({{ if .HasChecks }}checks {{ .Checks }}{{ end }}) (eval.Suite, error) {
{{- if .HasChecks }}
	if checks == nil { return eval.Suite{}, fmt.Errorf("evaluation checks are required") }
{{- end }}
	return eval.Suite{
		ID: {{ .ID }},
		Description: {{ .Description }},
		Scenarios: []eval.Scenario{
{{- range .Scenarios }}
			{
				ID: {{ .ID }},
				Description: {{ .Description }},
				Tags: []string{ {{- range .Tags }}{{ . }},{{- end }}},
				Timeout: time.Duration({{ .Timeout }}),
				Schema: {{ .Schema }},
				CheckNames: []string{ {{- range .Checks }}{{ .Name }},{{- end }}},
				Requirements: []eval.Requirement{
{{- range .Requirements }}
					{ID: {{ .ID }}, SchemaID: {{ .SchemaID }}, Statement: {{ .Statement }}, Subject: {{ .Subject }}, Evidence: []string{ {{- range .Evidence }}{{ . }},{{- end }}}, ForEach: {{ .ForEach }}, Scope: {{ .Scope }}, Reasoning: {{ .Reasoning }}},
{{- end }}
				},
				Bind: func(data rawjson.Message) (eval.Binding, error) {
					observed, err := {{ .Decode }}(data)
					if err != nil { return eval.Binding{}, err }
					binding := eval.Binding{Subjects: make(map[string][]eval.Subject)}
					{{ .Binding }}
{{- range .Checks }}
					{
						{{ .Binding }}
					}
{{- end }}
					return binding, nil
				},
			},
{{- end }}
		},
	}, nil
}

{{- range .Validators }}
// {{ .Name }} validates a generated evaluation value.
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
