// Package codegen generates direct, typed hook interfaces and immutable suite
// constructors from the generic evaluation DSL.
package codegen

import (
	"path/filepath"
	"strconv"
	"strings"

	evalexpr "goa.design/goa-ai/eval/expr"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

type (
	// suiteData contains fully evaluated values consumed by the suite template.
	suiteData struct {
		ID          string
		Description string
		Package     string
		Scenarios   []scenarioData
	}

	// scenarioData contains one generated hook method and immutable case data.
	scenarioData struct {
		ID          string
		Description string
		Input       string
		Method      string
		Tags        []string
		Timeout     int64
	}
)

func init() {
	goacodegen.RegisterPlugin("eval", "gen", nil, Generate)
}

// Generate emits one package per suite under gen/evals. Each package exposes a
// direct Hooks interface and a New constructor with no runtime registration or
// application adapter layer.
func Generate(_ string, roots []eval.Root, files []*goacodegen.File) ([]*goacodegen.File, error) {
	root := evaluationRoot(roots)
	if root == nil {
		return files, nil
	}
	for _, suite := range root.Suites {
		data := buildSuiteData(suite)
		imports := []*goacodegen.ImportSpec{
			{Path: "context"},
			{Path: "time"},
			{Path: "goa.design/goa-ai/eval", Name: "eval"},
		}
		sections := []*goacodegen.SectionTemplate{
			goacodegen.Header(suite.Name+" evaluation suite", data.Package, imports),
			{
				Name:   "evaluation-suite",
				Source: suiteTemplate,
				Data:   data,
			},
		}
		files = append(files, &goacodegen.File{
			Path:             filepath.Join(goacodegen.Gendir, "evals", suite.Name, "suite.go"),
			SectionTemplates: sections,
		})
	}
	return files, nil
}

// evaluationRoot locates the evaluated generic suite root supplied by Goa.
func evaluationRoot(roots []eval.Root) *evalexpr.RootExpr {
	for _, root := range roots {
		if suites, ok := root.(*evalexpr.RootExpr); ok {
			return suites
		}
	}
	return nil
}

// buildSuiteData partially evaluates all names, literals, durations, and tags
// so generated runtime code contains no static decisions.
func buildSuiteData(suite *evalexpr.SuiteExpr) suiteData {
	data := suiteData{
		ID:          strconv.Quote(suite.Name),
		Description: strconv.Quote(suite.Description),
		Package:     goacodegen.Goify(strings.ReplaceAll(suite.Name, "_", ""), false),
		Scenarios:   make([]scenarioData, len(suite.Scenarios)),
	}
	for i, scenario := range suite.Scenarios {
		timeout := scenario.Timeout
		if timeout == 0 {
			timeout = suite.Timeout
		}
		tags := make([]string, len(scenario.Tags))
		for j, tag := range scenario.Tags {
			tags[j] = strconv.Quote(tag)
		}
		data.Scenarios[i] = scenarioData{
			ID:          strconv.Quote(scenario.Name),
			Description: strconv.Quote(scenario.Description),
			Input:       strconv.Quote(scenario.Input),
			Method:      goacodegen.Goify(scenario.Name, true),
			Tags:        tags,
			Timeout:     int64(timeout),
		}
	}
	return data
}

const suiteTemplate = `// Hooks is implemented by the application running this evaluation suite.
// Methods may run concurrently and each owns its complete system interaction.
type Hooks interface {
{{- range .Scenarios }}
	// {{ .Method }} executes the {{ .ID }} scenario.
	{{ .Method }}(context.Context, string) (eval.Result, error)
{{- end }}
}

// New binds application hooks to the immutable generated suite definition.
func New(hooks Hooks) eval.Suite {
	return eval.Suite{
		ID: {{ .ID }},
		Description: {{ .Description }},
		Scenarios: []eval.Scenario{
{{- range .Scenarios }}
			{
				ID: {{ .ID }},
				Description: {{ .Description }},
				Input: {{ .Input }},
				Tags: []string{ {{- range .Tags }}{{ . }}, {{- end }}},
				Timeout: time.Duration({{ .Timeout }}),
				Run: hooks.{{ .Method }},
			},
{{- end }}
		},
	}
}
`
