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
		capturePath string
		assessPath string
		scenarios      {{ .ExampleValues }}
		tags           {{ .ExampleValues }}
	}
)

func {{ .ExampleMain }}() {
	opts := {{ .ExampleOptions }}{}
	flag.IntVar(&opts.maxConcurrency, "max-concurrency", 5, "maximum scenarios to run concurrently")
	flag.StringVar(&opts.capturePath, "capture", "", "capture evidence into a new archive without assessment")
	flag.StringVar(&opts.assessPath, "assess", "", "assess an existing archive without running product hooks")
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
	if opts.capturePath != "" && opts.assessPath != "" {
		return errors.New("--capture and --assess cannot be combined")
	}
	if opts.assessPath != "" && (len(opts.scenarios) > 0 || len(opts.tags) > 0) {
		return errors.New("--assess uses the scenarios recorded in the archive")
	}
	// TODO: for semantic requirements, call judge.New(client, maxOutputTokens)
	// from goa.design/goa-ai/eval/judge with your model.Client and a positive
	// per-response output-token limit; handle its returned error first.
	// Build eval.NewReasoningEngine(grader), handle its error, and pass the
	// engine to NewRunner. Alternatively, qualify a native classifier and
	// construct eval.NewSelectiveEngine with an explicit audit fraction.
	runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: opts.maxConcurrency})
	if err != nil { return err }
	var report eval.Report
	if opts.assessPath != "" {
		file, openErr := os.Open(opts.assessPath)
		if openErr != nil { return openErr }
		archive, readErr := eval.ReadArchive(file)
		if err := errors.Join(readErr, file.Close()); err != nil { return err }
		suite, buildErr := {{ .ExampleAlias }}.{{ .ForAssessment }}({{ if .HasChecks }}&{{ .ExampleHooks }}{}{{ end }})
		if buildErr != nil { return buildErr }
		report, err = runner.Assess(ctx, suite, archive)
	} else {
		suite, buildErr := {{ .ExampleAlias }}.{{ .New }}(&{{ .ExampleHooks }}{}, {{ .ExampleScenarioInputs }}())
		if buildErr != nil { return buildErr }
		var archive eval.Archive
		if opts.capturePath != "" {
			switch {
			case len(opts.scenarios) > 0:
				archive, err = runner.CaptureScenarios(ctx, suite, opts.scenarios...)
			case len(opts.tags) > 0:
				archive, err = runner.CaptureTags(ctx, suite, opts.tags...)
			default:
				archive, err = runner.Capture(ctx, suite)
			}
			if archive.ID == "" { return err }
			file, openErr := os.OpenFile(opts.capturePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if openErr != nil { return errors.Join(err, openErr) }
			_, writeErr := archive.WriteTo(file)
			if saveErr := errors.Join(err, writeErr, file.Close()); saveErr != nil { return saveErr }
			for _, observation := range archive.Observations {
				if observation.Error != "" {
					return fmt.Errorf("scenario %q capture failed: %s", observation.ScenarioID, observation.Error)
				}
			}
			return nil
		}
		switch {
		case len(opts.scenarios) > 0:
			archive, report, err = runner.RunScenarios(ctx, suite, opts.scenarios...)
		case len(opts.tags) > 0:
			archive, report, err = runner.RunTags(ctx, suite, opts.tags...)
		default:
			archive, report, err = runner.Run(ctx, suite)
		}
		if archive.ID == "" && err != nil { return err }
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
// {{ .Method }} executes {{ .RawID }} and returns its observed outcome.
// Use eval/evidence to collect tool calls and copy the facts needed by the
// declared checks and requirements into the typed observation.
func (*{{ $.ExampleHooks }}) {{ .Method }}(context.Context{{ if .HasInput }}, {{ .ExampleInputRef }}{{ end }}) ({{ .ExampleObservationRef }}, error) {
	return {{ .ObservationZero }}, errors.New("TODO: implement {{ .RawID }} capture")
}
{{- $scenario := . }}
{{- range .Checks }}
// {{ .Method }} checks saved evidence only. {{ .Description }}
func (*{{ $.ExampleHooks }}) {{ .Method }}(observed {{ $scenario.ExampleObservationRef }}) string {
	return "TODO: implement exact predicate"
}
{{- end }}

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
