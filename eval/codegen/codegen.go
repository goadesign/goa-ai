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
		ID           string
		Description  string
		Package      string
		Scenarios    []scenarioData
		Calibrations []calibrationData
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

	// calibrationData contains one immutable labeled judge example.
	calibrationData struct {
		ID     string
		Answer string
		Claim  string
		Want   string
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

// buildSuiteData partially evaluates all names, literals, durations, and labels
// so generated runtime code contains no static decisions.
func buildSuiteData(suite *evalexpr.SuiteExpr) suiteData {
	data := suiteData{
		ID:           strconv.Quote(suite.Name),
		Description:  strconv.Quote(suite.Description),
		Package:      goacodegen.Goify(strings.ReplaceAll(suite.Name, "_", ""), false),
		Scenarios:    make([]scenarioData, len(suite.Scenarios)),
		Calibrations: make([]calibrationData, len(suite.Calibrations)),
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
	for i, calibration := range suite.Calibrations {
		data.Calibrations[i] = calibrationData{
			ID:     strconv.Quote(calibration.Name),
			Answer: strconv.Quote(calibration.Answer),
			Claim:  strconv.Quote(calibration.Claim),
			Want:   labelConstant(string(calibration.Want)),
		}
	}
	return data
}

// labelConstant maps the closed DSL label vocabulary to generated constants.
func labelConstant(label string) string {
	switch label {
	case "entailed":
		return "eval.Entailed"
	case "contradicted":
		return "eval.Contradicted"
	case "not_addressed":
		return "eval.NotAddressed"
	case "indeterminate":
		return "eval.Indeterminate"
	default:
		panic("invalid evaluation label")
	}
}

const suiteTemplate = `// Hooks is implemented by the application running this evaluation suite.
// Each method owns the complete system interaction and returns typed evidence.
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
		Calibrations: []eval.Calibration{
{{- range .Calibrations }}
			{
				ID: {{ .ID }},
				Answer: {{ .Answer }},
				Claim: {{ .Claim }},
				Want: {{ .Want }},
			},
{{- end }}
		},
	}
}
`
