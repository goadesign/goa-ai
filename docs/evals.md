# Generated Evaluations

An evaluation (eval) is a repeatable test that runs your real product — usually
an AI agent — and checks that the outcome is still correct. Evaluations are not
unit tests: they call live systems and models, they can take minutes, and part
of "is this correct?" requires reading a model-written answer rather than
comparing exact values.

Goa-AI splits an evaluation suite into three parts, each with one owner:

- **The design** describes every scenario: its name, what it tests, the shape
  of its input, its tags, and its time limit. A design is the Go file where a
  Goa application already declares its services and agents; evaluation suites
  live in the same file set.
- **Generated code** turns that description into Go types and one interface
  method per scenario. If the design and the application drift apart, the
  build breaks instead of a test silently disappearing.
- **Your application code** implements those methods. It calls the product,
  gathers evidence, and states what must be true.

A runner from `goa.design/goa-ai/eval` executes the suite: it selects
scenarios, limits how many run at once, grades model answers, and produces a
JSON report.

These terms appear throughout:

- **Scenario**: one test case, such as "ask the assistant to list every
  record".
- **Hook**: the Go method you write for one scenario. It runs the product and
  returns what happened.
- **Check**: a pass/fail fact your code can verify exactly, such as "the agent
  called the `list_records` tool" or "every result page was fetched".
- **Claim**: a short English sentence that must be true of the model's answer,
  such as "The answer reports every record in the window." Claims exist because
  answer wording changes from run to run, so exact string comparison cannot
  work.
- **Judge**: a model-backed grader owned by the framework. It reads the answer
  and labels each claim as supported, contradicted, and so on.
- **Report**: the JSON summary of a run: what ran, what passed, why things
  failed, and how long everything took.

## Describe scenarios in the design

The design declares the *shape* of each scenario input: which fields exist and
what makes them valid. It never contains real values. Concrete requester IDs,
workspace IDs, and queries stay in application code, so the same design works in
every environment.

```go
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa-ai/eval/dsl"
	. "goa.design/goa/v3/dsl"
)

var RecordEvalInput = Type("RecordEvalInput", func() {
	Attribute("requester_id", String, "Requester running the evaluation.", func() {
		Format(FormatUUID)
	})
	Attribute("query", String, "Assistant request.", func() {
		MinLength(1)
	})
	Required("requester_id", "query")
})

var _ = Service("assistant_service", func() {
	Agent("assistant", "Answers user questions.", func() {
		Suite("assistant", func() {
			Description("Exercises assistant outcomes.")
			Timeout("2m")

			Scenario("record_inventory", func() {
				Description("Retrieves every record in a fixed window.")
				Input(RecordEvalInput)
				Tags("integration", "records")
				Timeout("3m")
			})

			Scenario("health_check", func() {
				Description("Verifies application-owned setup.")
			})
		})
	})
})
```

The rules are:

- Suite, scenario, and tag names use `lower_snake_case`. They become stable
  identifiers in reports and command-line flags, so renaming one renames the
  test everywhere.
- Every suite and scenario needs a `Description`. Every suite needs a positive
  `Timeout`; a scenario `Timeout` replaces the suite one for that scenario.
- `Input` is optional. A scenario without `Input` generates a hook that
  receives only a `context.Context`. `Input` accepts a named Goa type, a
  primitive, an array or map, or an inline function listing attributes.
  `OneOf` (a field that can hold one of several types) is not supported in
  evaluation inputs.
- A suite can be declared at the top level of the design or inside an `Agent`.
  Declaring it inside an agent additionally gives the generated package access
  to that agent's tool contracts (explained below).

## Generate the Go code

Importing `goa.design/goa-ai/eval/dsl` in the design registers the evaluation
generator. Running the normal `goa gen` command then writes
`gen/evals/<suite>/suite.go`:

```go
type RecordEvalInput struct {
	RequesterID string
	Query       string
}

type Hooks interface {
	RecordInventory(context.Context, *RecordEvalInput) (eval.Result, error)
	HealthCheck(context.Context) (eval.Result, error)
}

type Inputs struct {
	RecordInventory *RecordEvalInput
}

func New(hooks Hooks, inputs Inputs) (eval.Suite, error)
```

`Hooks` has one method per scenario, so adding a scenario to the design breaks
the build until the application implements it. `Inputs` has one field per
scenario that declared an `Input`; the application fills these with real
values. `New` checks every supplied value against the design rules (required
fields, formats, lengths) and returns an error before any scenario can start.

### Tool contracts for agent suites

Agents declare their tools in the design too, so the generator knows exactly
which tools an agent can call — including the tools of other agents it uses.
When a suite is declared inside an `Agent`, its generated package includes:

```go
func MustToolContract(name tools.Ident) *tools.ToolSpec
```

Given a tool name, it returns that tool's generated contract: its schema and
the codec that decodes its arguments and results. Use it in hooks to decode
recorded tool calls and check their arguments exactly, without writing JSON
handling by hand. It covers every tool the agent can reach at build time; it
does not cover tools discovered at runtime, whose contracts the generator
cannot know. Asking for a tool the agent cannot use panics, because that is a
bug in the evaluation itself.

## Create the runnable command

Run `goa example` after `goa gen`:

```bash
goa example example.com/product/design
```

This creates `cmd/<suite>-evals/main.go` once and never overwrites it — the
file belongs to the application from then on. Later design changes still
update `gen/evals`: a changed hook signature fails compilation and a missing
input value fails `New`, so the command cannot silently fall out of date.

The generated file compiles immediately and contains a `TODO` at every place
that needs application code: one empty hook per scenario, one input value per
scenario that declares an `Input`, and the judge. It also comes with a working
command line:

- `--scenario <id>` runs one scenario; repeat the flag for several.
- `--tag <tag>` runs every scenario carrying that tag; repeat for several.
  Scenario and tag flags cannot be combined.
- `--max-concurrency <n>` limits how many scenarios run at once (default 5).

Every run writes the JSON report to standard output and exits non-zero when
the suite fails. The command is a plain Go program, so it can run locally, in
CI, or on a schedule. Applications that prefer `go test` can skip the command
and call the generated `New` from a test instead.

## Write the hooks

Implement the generated interface on an ordinary type:

```go
type hooks struct {
	client *Client
}

func (h *hooks) RecordInventory(
	ctx context.Context,
	input *genevals.RecordEvalInput,
) (eval.Result, error) {
	answer, evidence, err := h.client.Run(ctx, input.RequesterID, input.Query)
	if err != nil {
		return eval.Result{}, err
	}
	return eval.Result{
		Checks: []eval.Check{{
			Name:   "all_pages_retrieved",
			Passed: evidence.Exhausted,
		}},
		Claims: []eval.Claim{{
			ID:   "complete_answer",
			Text: "The answer reports every record in the window.",
		}},
		Output: answer,
		Artifacts: []eval.Artifact{{
			Name: "protocol",
			URI:  evidence.ArtifactURI,
		}},
	}, nil
}
```

A hook returns three kinds of information:

- **Checks** are facts the code can verify exactly: tool names, IDs, counts,
  states. A failed check must include a diagnostic explaining what went wrong.
- **Claims** are sentences about the model's answer, judged later. Write one
  claim per fact ("The answer names the record", "The answer gives the
  creation time") rather than one long compound claim, so a failure points
  at the exact missing fact. Do not approximate answer meaning with regular
  expressions or keyword lists — that is what claims and the judge replace.
  Claims are judged against `Output`, the answer under evaluation. An empty
  `Output` — the run produced no answer — labels every claim `not_addressed`
  and fails the scenario without consulting the judge.
- **Artifacts** are optional links to saved evidence — logs, transcripts,
  screenshots — that help debug a failure.

Use the returned error only for infrastructure problems: the product could not
be reached, a timeout, a broken test environment. "The product answered but
the answer is wrong" is a failed check or a non-supported claim, not an error.

The runner rejects malformed results before scoring them: a result must
contain at least one check or claim, names and IDs must be unique, and
artifacts need both a name and a URI.

## Collect evidence and declare expectations

Most hooks do the same two things: watch a run's stream events to record what
the agent did, and compare that record against the scenario's expectations.
The `eval/evidence` package owns both so every suite shares one
implementation.

An `evidence.Collector` consumes the runtime's stream events (tool starts and
ends, assistant replies, workflow lifecycle, confirmation boundaries) and
builds an `evidence.Evidence`: every tool call with its canonical JSON
arguments and result, correlated by tool call ID and ordered causally (each
parent tool immediately before the nested calls its child run made), the
accumulated assistant answer, any pending confirmation, and the terminal
workflow phase. Applications that expose goa-ai streams natively feed events
straight in; applications that re-encode the stream over their own transport
write a small adapter that maps their wire type back to stream events.

```go
collector := evidence.NewCollector()
for !collector.Done() {
	event, err := stream.Recv()
	if err != nil {
		return eval.Result{}, err
	}
	if err := collector.Consume(event); err != nil {
		return eval.Result{}, err
	}
}
ev, err := collector.Finish()
```

An `evidence.Expect` declares the deterministic expectations and converts the
evidence into checks. Each generated toolset package exports one typed tool
descriptor per tool (for example `helpers.AnswerTool`) pairing the tool
identifier with its typed payload and result codecs. Build expectations from
descriptors with `evidence.ExpectCall`: the pairing is fixed at generation
time, assertions are typed Go predicates — no JSON traversal by hand — and a
design change that renames or retypes a field breaks the suite at compile
time instead of silently never matching:

```go
expect := evidence.Expect{
	Tools: []evidence.Tool{
		evidence.ExpectCall(helpers.AnswerTool,
			func(p *helpers.AnswerPayload) error {
				if p.Question == "" {
					return errors.New("question must not be empty")
				}
				return nil
			},
			nil, // result unconstrained
		),
	},
	ForbidTools: []tools.Ident{admin.DeleteRecords},
}
return eval.Result{
	Checks: expect.Checks(ev),
	Claims: claims,
	Output: ev.Answer,
}, nil
```

`Expect` supports two trajectory modes. The default binds the declared tools
as an in-order subsequence of the observed calls, leaving undeclared calls
unconstrained — a run may retry a rejected call or split work across several
calls of one tool. Setting `Exact: true` compares the complete causal
trajectory call for call, so hidden retries and extra tools fail. Per-tool
policies cover failure semantics: `evidence.ExpectFailure` declares a call
that must fail with exactly one classification, `ForbidFailureKinds` rejects
protected failure classes across every attempt, and
`RequireAllAttemptsSuccessful` rejects any failed or missing result.
`evidence.ExpectConfirmation` asserts the run stopped at a pending operator
confirmation instead of completing. For tools without generated descriptors
(registry-discovered toolsets), declare a bare `evidence.Tool` with the tool
identifier and `evidence.Decoded` asserts.

Bounded-result metadata is carried beside the typed result rather than inside
the generated domain result type. Set `Tool.Bounds` when the scenario must
assert the returned count, total count, truncation state, refinement hint, or
continuation cursor:

```go
recordCall := evidence.ExpectCall(records.ListRecordsTool, nil, nil)
recordCall.Bounds = func(bounds *agent.Bounds) error {
	if bounds == nil || bounds.Truncated {
		return errors.New("expected a complete record inventory")
	}
	return nil
}
```

## Run the suite

```go
suite, err := genevals.New(&hooks{client: client}, genevals.Inputs{
	RecordInventory: &genevals.RecordEvalInput{
		RequesterID: requesterID,
		Query:       "List every record in the requested window.",
	},
})
if err != nil {
	return err
}

grader, err := judge.New(modelClient, maxOutputTokens)
if err != nil {
	return err
}
runner, err := eval.NewRunner(grader, eval.RunnerConfig{
	MaxConcurrency: 5,
	Reporter:       reporter,
})
if err != nil {
	return err
}
report, err := runner.Run(ctx, suite)
```

`MaxConcurrency` is required and must be positive: at most that many scenarios
run at the same time. One scenario failing does not stop the others. The
report always lists scenarios in the order the design declares them, no matter
which finished first. Because scenarios run in parallel, hooks and the judge
must be safe to call concurrently.

The optional `Reporter` receives a callback when each scenario starts and when
it finishes, so an application can print progress without scheduling anything
itself. Every selected scenario gets exactly one finished callback — including
scenarios that never started because the run was canceled (those have a zero
start time and no started callback).

Canceling the context stops new scenarios from starting and cancels the ones
in flight through their own contexts; `Run` then returns the context error
together with the partial report. Hooks must honor cancellation.

Pass a nil judge only when no hook returns claims:

```go
runner, err := eval.NewRunner(nil, eval.RunnerConfig{MaxConcurrency: 2})
```

If a hook returns a claim and the judge is nil, that scenario fails with an
error saying a judge is required.

### Select scenarios

The runner validates every selection before calling the product or a model:

```go
report, err := runner.Run(ctx, suite)
report, err := runner.RunScenarios(ctx, suite, "record_inventory", "summary_check")
report, err := runner.RunTags(ctx, suite, "smoke", "records")
```

`RunScenarios` runs exact scenario names. `RunTags` runs every scenario
carrying at least one of the given tags. Both reject empty selections, empty
values, duplicates, and names or tags that do not exist, so a typo fails
loudly instead of silently running nothing.

## How judging works

`eval/judge` builds a judge from any `model.Client`, the same model-client
interface the rest of Goa-AI uses, so it works with any configured provider.
The application must supply a positive `maxOutputTokens` to `judge.New` and
handle its construction error. This is the inclusive output-token limit for one
complete model response, shared by every judgment and the tool JSON, not a
per-claim or whole-suite allowance. Each permitted correction receives the same
limit. The judge never multiplies it by claim count, clips it to a provider
ceiling, or increases it after failure. Provider limits still apply, and any
finite allowance can be exhausted without a usable judgment. Choose the value
in application configuration alongside the model and acceptable resource use;
there is no framework default or promise that a given limit will suffice.

This replaces `judge.New(modelClient, opts...)`, which returned only a judge and
assigned 256 output tokens per claim. On upgrade, pass your explicit response
limit as the second argument, handle the returned error, and keep options such
as `judge.WithModelClass(...)` after that argument. This constructor migration
does not change generated suites, stored reports, or custom `eval.Judge`
implementations.

Custom judges implement this same batch contract:

```go
Judge(ctx context.Context, output string, claims []eval.Claim, reference string) ([]eval.Judgment, error)
```

The output is supplied once for the whole ordered claim list. Reports and
judgments retain claim IDs. The model-backed judge uses those IDs as required
JSON property names, not identifier values that the model must copy between
separate lists or calls.

Hooks put shared factual context in `Result.Reference`, rather than repeating it
inside several claims. The runner passes that string separately from the unchanged
answer and retains it as `reference` in the JSON report; an empty reference is
omitted and means no additional context is needed. The judge supplies it once per
request, including corrections. Reference facts may establish whether the answer
is accurate, but cannot supply content the answer omitted. An empty answer still
receives `not_addressed` labels without a judge call, even with a rich reference.

Custom judges and direct callers must adopt the fourth argument; pass `""` when
no reference is needed. Calibration uses an empty reference. Existing reports
without `reference` retain that meaning; no generated suite or product service
contract changes. Applications with strict external report readers must accept
the new report field before consuming reports that include it.

For each scenario the judge receives the answer and the scenario's claims, and
returns exactly one label and a short rationale per claim:

- `entailed`: the answer establishes the claim is true;
- `contradicted`: the answer establishes the claim is false;
- `not_addressed`: the answer talks about something else;
- `indeterminate`: the answer is too ambiguous or conflicting to decide.

Only `entailed` counts as passing.

Before any scenario runs, the runner tests the judge with four fixed examples,
one per label. This step is called calibration. A judge that cannot tell the
labels apart — for example one that answers `entailed` for everything, which
would make every evaluation pass — fails calibration and the suite stops
before touching the application. Calibration runs under a two-minute deadline
owned by the runner, so an unreachable or stalled model endpoint fails the
suite with a clear error instead of blocking it forever.

The judge sends the answer and, when present, one shared reference in its user
message. Its prompt asks for the supplied grading tool rather than spelling a
provider-specific tool name. The private `eval.submit_judgments` tool requires
one property per claim ID, with the exact claim
text appearing once as that property's description. Each property contains a
required label and nonempty rationale. For example, claims named `subject` and
`severity` produce `{"subject":{"label":"entailed","rationale":"..."},
"severity":{"label":"not_addressed","rationale":"..."}}`. JSON property
order is irrelevant: the judge looks up each claim by name and returns judgments
in the input claim order. It never assigns a result by list position.

The schema rejects unknown or missing claim names, unknown fields, invalid labels,
and empty rationales. The judge's raw JSON codec also rejects duplicate decoded
member names at every object depth, including escaped spellings of the same name,
before map decoding could discard one decision. These invalid arguments use the
runtime's existing bounded correction flow through `runtime/agent/tooloutput.Run`:
one initial call and at most three corrections, with the same response allowance.
Provider and transport failures are not retried by the judge. There is no new
native strict-output requirement or per-claim model call.

The judge's tool schema also describes the required object structure and carries
field metadata for each claim, label, and rationale. The existing validator uses
that metadata to provide precise correction guidance: an extra root property is
identified as an undeclared field, while a judgment encoded as a JSON string is
instructed to become a JSON object. A claim named `requests` remains valid when
that name is required by the schema; there is no reserved-name filter.

Full claim text and reference evidence remain available to the judge unchanged.
Correction metadata describes structure only, so even a long claim is not copied
into the limited-size feedback. The judge adds no example labels or rationales
that could influence the semantic decision. This changes neither grading rules,
accepted labels, model selection, token limits, nor the existing correction count;
it improves guidance without guaranteeing that the model will follow it.

Claim IDs remain unique, nonempty strings. The model-backed judge additionally
requires valid UTF-8 so JSON encoding cannot silently change their names; it adds
no naming pattern, trimming, or case normalization. Full claim texts count toward
the existing 1 MiB tool-schema limit. An oversized schema fails before inference;
claims and evidence are never truncated or automatically split across calls.

Named properties replace the private positional-array tool protocol without
changing generated suites, calibration, or labels. They enforce association and
coverage, not semantic
truth: a rationale about the wrong fact under a valid property is still a model
judgment error. The framework preserves its label and rationale exactly; it does
not repair, reassign, or approve the decision.

When judging fails, the existing wrapped error retains the private run's original
causes and full diagnostic messages. The runner includes that text in
`ScenarioReport.Error`; it does not turn a failed judge call into semantic labels.
See [typed-output diagnostics](runtime.md#forced-typed-tool-output) for available
response facts and cancellation behavior. A valid `contradicted` judgment is
still a successful judge call, although its scenario does not pass.

## Read the report

The report and everything in it use stable JSON field names, so tooling can
depend on them. A scenario's duration covers the hook call, result validation,
and judging.

Failures land at two levels:

- **Suite-level** failures — an invalid selection, a calibration failure, or
  cancellation — are returned as the error from `Run` and recorded on the
  report's `error` field.
- **Scenario-level** failures — a hook error, an invalid result, a timeout, or
  a judging failure — are recorded on that scenario's report so the remaining
  scenarios still finish.

After a run without a suite-level error, check `report.Passed`: it is true
only when every selected scenario passed all of its checks and claims. A false
value must fail the calling command or CI job.

## Upgrade from string-input suites

Earlier versions passed a single string to every hook. Typed inputs replace
that:

- replace `Input("some literal")` with a Goa input type and move the literal
  into the generated `Inputs` value;
- use Goa v3 `Description` and `Timeout`, plus Goa-AI `Tags`; `eval/dsl` now
  declares only `Suite`, `Scenario`, and `Input`;
- update hooks from `(context.Context, string)` to their generated typed
  signatures; and
- update `New(hooks)` to `New(hooks, inputs)` and handle its validation error.

Regenerate before compiling application code. Generated suite packages and
application hooks live in one Go binary, so there is no version-mixing concern
across a network: a mismatch is a compile error, not a runtime surprise.
