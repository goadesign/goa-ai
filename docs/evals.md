# Generated Evaluations

Goa-AI evaluations separate stable suite structure from application-specific
execution. The framework owns design, code generation, orchestration, semantic
classification, and reports. The application owns targets, system interaction,
deterministic evidence, and domain claims.

## Design

Import `goa.design/goa-ai/eval/dsl` from a Goa design package:

```go
var _ = Suite("chat", func() {
	Description("Exercises production Chat outcomes.")
	Timeout("2m")

	Scenario("alarm_inventory", func() {
		Description("Retrieves every alarm in a fixed window.")
		Input("List every alarm in the requested window.")
		Tags("production", "alarm")
		Timeout("3m")
	})

	Calibration("entailed", func() {
		Answer("Compressor 1 is on.")
		Claim("Compressor 1 is on.")
		Want(eval.Entailed)
	})
})
```

Suite, scenario, calibration, and tag IDs are lower snake case. Descriptions,
inputs, and positive timeouts are required. Scenario timeouts override the
suite default. Generated hook method names must be unique after Go naming.
Calibrations use the closed labels `entailed`, `contradicted`, `not_addressed`,
and `indeterminate`.

Importing `goa.design/goa-ai/eval/dsl` registers the eval codegen plugin. `goa gen`
emits `gen/evals/<suite>/suite.go` with a direct interface and constructor:

```go
type Hooks interface {
	AlarmInventory(context.Context, string) (eval.Result, error)
}

func New(hooks Hooks) eval.Suite
```

Static names, prompts, tags, timeouts, calibrations, and dispatch decisions are
fully evaluated during generation. Generated code uses no reflection or runtime
registration.

## Application hooks

Implement the generated interface on an ordinary application struct:

```go
type hooks struct {
	client *Client
	target Target
}

func (h *hooks) AlarmInventory(ctx context.Context, input string) (eval.Result, error) {
	answer, evidence, err := h.client.Run(ctx, h.target, input)
	if err != nil {
		return eval.Result{}, err
	}
	return eval.Result{
		Checks: []eval.Check{{Name: "complete_page", Passed: evidence.Exhausted}},
		Claims: []eval.Claim{{ID: "total", Text: "The answer reports every alarm."}},
		Output: answer,
	}, nil
}
```

Methods and closures naturally capture application dependencies and targets.
Do not introduce adapter registries or pass application target concepts through
the generic DSL.

`Result.Checks` are deterministic assertions over application-owned typed
evidence. `Result.Claims` are independent propositions classified against
`Result.Output`. Infrastructure and protocol failures are returned as errors.
Artifacts carry durable diagnostic locations. Results with neither checks nor
claims, duplicate IDs, claims without output, malformed checks, or malformed
artifacts are rejected at the hook boundary.

## Semantic judge

`eval/judge` accepts any provider-neutral `model.Client`. It sends one
tool-free, structured-output request for each assertion batch and accepts only
one exact judgment per claim ID. Unknown fields, missing judgments, duplicate
IDs, unsupported labels, multiple content parts, and trailing JSON are errors;
the judge never retries, repairs, or substitutes output.

Before any scenario runs, `Runner` submits all generated calibrations in one
batch and verifies their exact expected labels. A calibration failure aborts
the suite. Scenario claims pass only when each judgment is `entailed`.

## Running and reporting

```go
suite := genevals.New(hooks)
report, err := eval.NewRunner(judge).Run(ctx, suite)
```

Scenarios execute sequentially under their generated timeouts. Passing requires
at least one selected scenario and every deterministic check and semantic claim
to pass. Hook, validation, and judge failures are recorded on the scenario
report; selection and calibration failures are recorded on the suite report.
`Report` and its nested values have stable JSON field names for CI and retention
systems.

An application may pass tags to `Runner.Run` to select scenarios. Product CLIs
can also select exact generated scenario IDs before invoking the runner, while
preserving declaration order. Selection must fail before execution when an ID
is unknown.
