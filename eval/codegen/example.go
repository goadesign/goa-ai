// Package codegen generates typed evaluation suites and application scaffolds.
// This file owns create-once application scaffolds produced by goa example.
package codegen

import (
	"goa.design/goa/v3/codegen"
)

func exampleFile(path string, data *exampleData) *codegen.File {
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
					Imports: data.FileImports,
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
// This file was generated once by goa example. Later generation does not
// replace it, so changes to product calls and checks remain in this file.
package main

import (
{{- range .Imports }}
	{{ if .Name }}{{ .Name }} {{ end }}{{ printf "%q" .Path }}
{{- end }}
)

`

const exampleTemplate = `type (
	{{ .ExampleHooks }} struct{}

	{{ .ExampleValues }} []string

	{{ .ExampleOptions }} struct {
		maxConcurrency int
		scenarios      {{ .ExampleValues }}
		tags           {{ .ExampleValues }}
	}
)

func {{ .ExampleMain }}() {
	opts := {{ .ExampleOptions }}{}
	flag.IntVar(&opts.maxConcurrency, "max-concurrency", 5, "maximum scenarios to run concurrently")
	flag.Var(&opts.scenarios, "scenario", "scenario ID to run; repeat to select multiple scenarios")
	flag.Var(&opts.tags, "tag", "scenario tag to run; repeat to select multiple tags")
	flag.Parse()
	if err := {{ .ExampleRun }}(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func {{ .ExampleRun }}(ctx context.Context, opts {{ .ExampleOptions }}) error {
	if len(opts.scenarios) > 0 && len(opts.tags) > 0 {
		return errors.New("--scenario and --tag cannot be combined")
	}
	suite, err := {{ .ExampleAlias }}.{{ .New }}(&{{ .ExampleHooks }}{}, {{ .ExampleScenarioInputs }}())
	if err != nil {
		return err
	}
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{
		MaxConcurrency: opts.maxConcurrency,
		// TODO: replace nil above with an eval.Judge when a model must grade
		// hook results. Create judge.New (goa.design/goa-ai/eval/judge) with
		// your real model.Client. Nil is valid when code performs every check.
		// TODO: supply an eval.Reporter if results should be shown as tests finish.
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

// {{ .ExampleScenarioInputs }} supplies environment-specific values. Replace every TODO
// before running the suite.
func {{ .ExampleScenarioInputs }}() {{ .ExampleAlias }}.{{ .Inputs }} {
	return {{ .ExampleAlias }}.{{ .Inputs }}{
{{- range .Scenarios }}
	{{- if .HasInput }}
		{{ .InputField }}: {{ .InputZero }}, // TODO: supply the {{ .RawID }} input.
	{{- end }}
{{- end }}
	}
}

{{- range .Scenarios }}
// {{ .Method }} executes the {{ .RawID }} scenario. Run the product flow,
// then check the result. Use goa.design/goa-ai/eval/evidence to collect tool
// calls, use evidence.ExpectCall with the generated tool descriptions to state
// which calls are required, and return both checks performed by code and checks
// that require a model to judge their meaning.
func (*{{ $.ExampleHooks }}) {{ .Method }}(context.Context{{ if .HasInput }}, {{ .ExampleInputRef }}{{ end }}) (eval.Result, error) {
	return eval.Result{}, errors.New("TODO: implement {{ .RawID }}")
}

{{- end }}
func (v *{{ .ExampleValues }}) String() string {
	return fmt.Sprint([]string(*v))
}

func (v *{{ .ExampleValues }}) Set(value string) error {
	if value == "" {
		return errors.New("selector must not be empty")
	}
	*v = append(*v, value)
	return nil
}
`
