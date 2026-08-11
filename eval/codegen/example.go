// Package codegen generates typed evaluation suites and application scaffolds.
// This file owns create-once application scaffolds produced by goa example.
package codegen

import (
	"path/filepath"

	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

// GenerateExample emits one create-once command for every evaluation suite.
// The file lives outside gen/ and is never overwritten after application code
// fills in product execution, fixture values, judge wiring, and reporting.
func GenerateExample(genpkg string, roots []eval.Root, files []*codegen.File) ([]*codegen.File, error) {
	root := evaluationRoot(roots)
	if root == nil {
		return files, nil
	}
	for _, suite := range root.Suites {
		path := filepath.Join("cmd", suite.Name+"-evals", "main.go")
		data, err := buildSuiteData(genpkg, suite)
		if err != nil {
			return nil, err
		}
		files = append(files, exampleFile(path, data))
	}
	return files, nil
}

func exampleFile(path string, data *suiteData) *codegen.File {
	imports := []*codegen.ImportSpec{
		{Path: "context"},
		{Path: "encoding/json"},
		{Path: "errors"},
		{Path: "flag"},
		{Path: "fmt"},
		{Path: "os"},
		{Name: data.ExampleAlias, Path: data.ExamplePackage},
		{Path: "goa.design/goa-ai/eval", Name: "eval"},
	}
	return &codegen.File{
		Path:      path,
		SkipExist: true,
		SectionTemplates: []*codegen.SectionTemplate{
			{
				Name:   "evaluation-example-header",
				Source: exampleHeaderTemplate,
				Data: struct {
					Suite   string
					Imports []*codegen.ImportSpec
				}{
					Suite:   data.Name,
					Imports: imports,
				},
			},
			{
				Name:   "evaluation-example",
				Source: exampleTemplate,
				Data:   data,
			},
		},
	}
}

const exampleHeaderTemplate = `// Command {{ .Suite }}-evals runs the {{ .Suite }} evaluation suite.
//
// This file was generated once by goa example. It is application-owned:
// subsequent generation leaves product execution and assertions unchanged.
package main

import (
{{- range .Imports }}
	{{ if .Name }}{{ .Name }} {{ end }}{{ printf "%q" .Path }}
{{- end }}
)

`

const exampleTemplate = `type (
	hooks struct{}

	values []string

	options struct {
		maxConcurrency int
		scenarios      values
		tags           values
	}
)

func main() {
	opts := options{}
	flag.IntVar(&opts.maxConcurrency, "max-concurrency", 5, "maximum scenarios to run concurrently")
	flag.Var(&opts.scenarios, "scenario", "scenario ID to run; repeat to select multiple scenarios")
	flag.Var(&opts.tags, "tag", "scenario tag to run; repeat to select multiple tags")
	flag.Parse()
	if err := run(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if len(opts.scenarios) > 0 && len(opts.tags) > 0 {
		return errors.New("--scenario and --tag cannot be combined")
	}
	suite, err := {{ .ExampleAlias }}.New(&hooks{}, scenarioInputs())
	if err != nil {
		return err
	}
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{
		MaxConcurrency: opts.maxConcurrency,
		// TODO: replace nil above with an eval.Judge when hooks return semantic
		// claims: construct judge.New (goa.design/goa-ai/eval/judge) with your
		// real model.Client. Nil is valid only for deterministic-only suites.
		// TODO: supply an eval.Reporter for progressive application-specific output.
	})
	if err != nil {
		return err
	}
	var report eval.Report
	switch {
	case len(opts.scenarios) > 0:
		report, err = runner.RunScenarios(ctx, suite, opts.scenarios...)
	case len(opts.tags) > 0:
		report, err = runner.RunTags(ctx, suite, opts.tags...)
	default:
		report, err = runner.Run(ctx, suite)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if encodeErr := encoder.Encode(report); encodeErr != nil {
		return errors.Join(err, fmt.Errorf("encode evaluation report: %w", encodeErr))
	}
	if err != nil {
		return err
	}
	if !report.Passed {
		return fmt.Errorf("evaluation suite %q failed", report.SuiteID)
	}
	return nil
}

// scenarioInputs supplies environment-specific values. Replace every TODO
// before running the suite.
func scenarioInputs() {{ .ExampleAlias }}.Inputs {
	return {{ .ExampleAlias }}.Inputs{
{{- range .Scenarios }}
	{{- if .HasInput }}
		{{ .InputField }}: {{ .InputZero }}, // TODO: supply the {{ .RawID }} input.
	{{- end }}
{{- end }}
	}
}

{{- range .Scenarios }}
// {{ .Method }} executes the {{ .RawID }} scenario.
func (*hooks) {{ .Method }}(context.Context{{ if .HasInput }}, {{ .ExampleInputRef }}{{ end }}) (eval.Result, error) {
	return eval.Result{}, errors.New("TODO: implement {{ .RawID }}")
}

{{- end }}
func (v *values) String() string {
	return fmt.Sprint([]string(*v))
}

func (v *values) Set(value string) error {
	if value == "" {
		return errors.New("selector must not be empty")
	}
	*v = append(*v, value)
	return nil
}
`
