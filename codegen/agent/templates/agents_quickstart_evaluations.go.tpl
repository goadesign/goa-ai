---

## 8. 🧪 Evaluating Your Agents
{{- if .Suites }}

An evaluation suite is a set of stable scenarios that exercise your agent and grade the outcome, so you can catch regressions before your users do. Your design declares the following suite{{ if gt (len .Suites) 1 }}s{{ end }}:
{{- range .Suites }}

### Suite `{{ .Name }}`{{ if .Agent }} (agent `{{ .Agent }}`){{ end }}

{{ .Description }}
{{- $suite := . }}
{{- range .Scenarios }}

* **`{{ .Name }}`**{{ if .Tags }} (tags: {{ range $i, $t := .Tags }}{{ if $i }}, {{ end }}`{{ $t }}`{{ end }}){{ end }}: {{ .Description }}{{ if .HasInput }} Supply its typed input when constructing the suite in `cmd/{{ $suite.Name }}-evals`.{{ end }}
{{- end }}

Everything typed lives in `gen/evals/{{ .Name }}/`: a `Hooks` interface with one method per scenario, an `Inputs` struct for typed inputs, and (for agent-attached suites) `MustToolContract` to assert against the agent's reachable tool contracts. `goa example` scaffolds an application-owned command at `cmd/{{ .Name }}-evals/main.go` **once**; it is yours to edit and is never overwritten.

Implement each hook to run the real agent, then return the evidence to grade:

* **Checks** are deterministic facts: which tools were called, with which arguments, whether the run completed.
* **Claims** are plain-language statements about the reply ("the answer names the capital of Japan") graded by a model judge.

Don't recompute trajectory facts by hand. Bridge the runtime's event bus into
an `evidence.Collector` (package `goa.design/goa-ai/eval/evidence`) while the
agent runs, then declare expectations with the generated typed tool
descriptors — the predicates are compile-checked against the tool's actual
payload and result types:

```go
collector := evidence.NewCollector()
sub, err := streambridge.Register(rt.Bus, sink, stream.RuntimeHostProfile()) // sink filters the scenario's session and calls collector.Consume
// ... run the agent ...
ev, err := collector.Finish()
expect := evidence.Expect{
    Tools: []evidence.Tool{
        evidence.ExpectCall(specs.<Tool>Tool(),
            func(p *specs.<ToolPayload>) error { /* assert arguments */ return nil },
            func(r *specs.<ToolReturn>) error { /* assert result */ return nil },
        ),
    },
}
return eval.Result{Checks: expect.Checks(ev), Claims: claims, Output: ev.Answer}, nil
```

`Expect` grades the causal trajectory (in-order subsequence by default,
`Exact: true` for call-for-call equality), forbidden tools, failure
classifications (`evidence.ExpectFailure`), pending operator confirmations
(`evidence.ExpectConfirmation`), and the terminal workflow phase.

```bash
go run ./cmd/{{ .Name }}-evals                    # run every scenario, JSON report on stdout
go run ./cmd/{{ .Name }}-evals -scenario <id>     # run selected scenarios (repeatable)
go run ./cmd/{{ .Name }}-evals -tag <tag>         # run by tag (repeatable)
```

The command exits non-zero when any scenario fails, so it drops straight into CI. Deterministic-only suites can pass a `nil` judge; as soon as a hook returns claims, call `judge.New(modelClient, maxOutputTokens)` (see `goa.design/goa-ai/eval/judge`) and handle its returned error. Supply a positive output-token limit for one complete response, shared by all claims and applied unchanged to each permitted correction. The limit does not guarantee completion.
{{- end }}
{{- else }}

Your design doesn't declare any evaluation suites yet. An evaluation suite is a set of stable scenarios that exercise your agent and grade the outcome, so you can catch regressions before your users do. Declare one inside an `Agent` block:

```go
import . "goa.design/goa-ai/eval/dsl"

Agent("chat", "Friendly Q&A assistant", func() {
    // ... toolsets ...
    Suite("chat_quality", func() {
        Description("Evaluates the chat agent end to end.")
        Scenario("greeting_reply", func() {
            Description("The agent produces a final reply to a greeting.")
            Tags("smoke")
        })
    })
})
```

Rerun `goa gen` to get a typed harness under `gen/evals/<suite>/` (one hook per scenario) and `goa example` to scaffold a runnable `cmd/<suite>-evals` command. Each hook runs your real agent and returns deterministic checks plus model-graded claims.
{{- end }}

