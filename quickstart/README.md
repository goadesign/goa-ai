# Goa‑AI Quickstart

A runnable agent loop, typed completions, and an evaluation suite. The checked-in
example uses deterministic responses and in-memory execution, so no model
credentials or external services are needed. Keep your design in `design/`;
never edit `gen/`.

## Prerequisites

- Go 1.26.0+

## 1) Run the checked-in project

```bash
git clone https://github.com/goadesign/goa-ai.git
cd goa-ai/quickstart
go run ./cmd/orchestrator
```

The quickstart's module uses the Goa-AI source in the parent checkout and its
pinned Goa dependency. The generation commands below use that same version.

## 2) Read the design (design/design.go)

This declares one service (`orchestrator`) with a single agent (`chat`), a tiny
helper toolset, a typed direct completion, and an evaluation suite.

```go
package design

import (
    . "goa.design/goa/v3/dsl"
    . "goa.design/goa-ai/dsl"
    . "goa.design/goa-ai/eval/dsl"
)

var _ = API("orchestrator", func() {})

// Input and output types with inline descriptions (required by this repo style)
var AskPayload = Type("AskPayload", func() {
    Attribute("question", String, "User question to answer")
    Example(map[string]any{"question": "What is the capital of Japan?"})
    Required("question")
})

var Answer = Type("Answer", func() {
    Attribute("text", String, "Answer text")
    Example(map[string]any{"text": "Tokyo is the capital of Japan."})
    Required("text")
})

var CapturedToolFailure = Type("CapturedToolFailure", func() {
    Attribute("kind", String, "Observed failure classification.")
    Attribute("message", String, "Observed failure explanation.")
    Attribute("recovery_action", String, "The runtime's recorded recovery action.")
    Required("kind", "message", "recovery_action")
})

var CapturedToolCall = Type("CapturedToolCall", func() {
    Attribute("name", String, "The invoked tool.")
    Attribute("call_id", String, "The identifier linking invocation and result.")
    Attribute("parent_call_id", String, "The parent call, or empty for a root call.")
    Attribute("arguments", Bytes, "Exact tool-argument JSON bytes.")
    Attribute("result", Bytes, "Exact result JSON bytes, when available.")
    Attribute("completed", Boolean, "Whether a terminal tool result was observed.")
    Attribute("failure", CapturedToolFailure, "Observed failure, when the tool failed.")
    Required("name", "call_id", "parent_call_id", "completed")
})

var GreetingObservation = Type("GreetingObservation", func() {
    Attribute("question", String, "The original question sent to the agent.")
    Attribute("answer", String, "Captured assistant text, including an empty answer.")
    Attribute("tool_calls", ArrayOf(CapturedToolCall), "Invocations in the collector's causal order.")
    Attribute("terminal_phase", String, "Observed final workflow phase, or empty when absent.")
    Attribute("terminal_failure", String, "Observed workflow failure explanation, or empty.")
    Attribute("run_error", String, "The product call's returned error, or empty on success.")
    Required("question", "answer", "terminal_phase", "terminal_failure", "run_error")
})

var DraftTaskStep = Type("DraftTaskStep", func() {
    Attribute("title", String, "Short step title")
    Example(map[string]any{"title": "Review the current launch checklist"})
    Required("title")
})

var TaskDraft = Type("TaskDraft", func() {
    Attribute("assistant_text", String, "Short explanation of the generated draft")
    Attribute("name", String, "Task name")
    Attribute("goal", String, "Outcome-style goal")
    Attribute("steps", ArrayOf(DraftTaskStep), "Ordered draft steps")
    Example(map[string]any{
        "assistant_text": "Created a launch-readiness task draft.",
        "name": "Prepare launch checklist",
        "goal": "Confirm the service is ready to launch.",
        "steps": []map[string]any{
            {"title": "Review release notes and rollout scope"},
            {"title": "Confirm dashboards and alerts are healthy"},
            {"title": "Share the launch checklist with stakeholders"},
        },
    })
    Required("assistant_text", "name", "goal", "steps")
})

var _ = Service("orchestrator", func() {
    Completion("draft_task", "Produce a task draft directly", func() {
        Return(TaskDraft)
    })

    Agent("chat", "Friendly Q&A assistant", func() {
        Use("helpers", func() {
            Tool("answer", "Answer a simple question", func() {
                Args(AskPayload)
                Return(Answer)
            })
        })
        RunPolicy(func() {
            DefaultCaps(MaxToolCalls(2), MaxRecoveryTurns(1))
            TimeBudget("15s")
        })

        Suite("chat_quality", func() {
            Description("Evaluates the chat agent end to end against the in-memory runtime.")
            Timeout("30s")
            Scenario("greeting_reply", func() {
                Description("The agent produces a final assistant reply to a user question.")
                Input(AskPayload)
                Observation(GreetingObservation)
                Check("run_tools", "The agent completes and answers the original question through helpers.answer.")
                Tags("smoke")
            })
            Scenario("helpers_contract", func() {
                Description("The helpers.answer tool contract is reachable from the agent.")
                Observation(Boolean)
                Check("payload_schema", "The reachable helpers.answer contract includes a payload schema.")
                Tags("contract")
            })
        })
    })
})
```

## 3) Generate code and example

```bash
go run goa.design/goa/v3/cmd/goa gen example.com/quickstart/design
go run goa.design/goa/v3/cmd/goa example example.com/quickstart/design
```

This creates:

- **`gen/`** - Generated code (never edit by hand), including one typed
  descriptor factory per tool (`helpers.AnswerTool()`) pairing the tool identifier with
  its payload and result codecs
- **`cmd/orchestrator/main.go`** - Runnable example using the bootstrap
- **`internal/agents/orchestrator/bootstrap/bootstrap.go`** - Wires runtime, registers agents and toolset executors
- **`internal/agents/chat/planner/planner.go`** - Application-owned planner (edit to connect your LLM)
- **`gen/<service>/completions/`** - Generated typed direct-completion helpers
- **`gen/evals/chat_quality/`** - Typed capture hooks, exact predicates, validated inputs, observation codecs, and tool contracts
- **`cmd/chat_quality-evals/main.go`** - Application-owned eval command (created once, never overwritten)

This quickstart's application-owned files are already filled in to demonstrate
the full agent loop deterministically, with no model or external service:

- The planner (`internal/agents/chat/planner/planner.go`) requests the
  `helpers.answer` tool with the user's question on `PlanStart`.
- The executor (`internal/agents/chat/toolsets/helpers/execute.go`) decodes
  the payload and example result with `helpers.AnswerTool()`, then returns the
  typed `AnswerResult`. Invalid payloads come back as classified invalid-call
  failures with structured correction guidance. The runtime passes the typed
  result to `PlanResume`, where the planner finalizes with the answer.
- The bootstrap registers the executor with the generated
  `RegisterUsedToolsets` helper, which fails fast when an executor is missing.

### Typed Direct Completion

Tools are for callable capabilities. When you want the assistant to return a
typed result directly, declare a service-owned completion:

```go
var TaskDraft = Type("TaskDraft", func() {
    Attribute("name", String, "Task name")
    Attribute("goal", String, "Outcome-style goal")
    Required("name", "goal")
})

var _ = Service("orchestrator", func() {
    Completion("draft_task", "Produce a task draft directly", func() {
        Return(TaskDraft)
    })
})
```

Completion names are part of the structured-output contract. They must be
1-64 ASCII characters, may contain letters, digits, `_`, and `-`, and must
start with a letter or digit.

Regeneration emits `gen/orchestrator/completions/` with the result schema and
generated helpers such as `CompleteDraftTask(...)` and
`StreamCompleteDraftTask(...)`. Codec details stay inside that generated
package.

The unary helper issues a unary model request with provider-enforced structured
output. The validated client compiles the generated schema before provider work,
checks the returned JSON itself, and then the helper decodes it through the
generated codec. The streaming helper returns a typed stream:
`completion_delta` chunks are preview-only. The low-level client retains the
final `completion` chunk until the provider stream ends, both final
representations satisfy the schema, and their JSON bytes match. `Value()` then
becomes available after generated decoding. Generated completion helpers reject
tool-enabled requests and caller-supplied `StructuredOutput`. Providers that do
not implement structured output return `model.ErrStructuredOutputUnsupported`.

## 4) Run the generated example

```bash
go run ./cmd/orchestrator
```

Expected output:

```
RunID: orchestrator-chat-...
Assistant: Tool helpers.answer returned {"text":"Tokyo is the capital of Japan."}
Completion draft_task: ...
Completion delta draft_task: ...
Completion stream draft_task: ...
```

The assistant reply proves the whole loop ran: planner → helpers.answer tool
→ executor → planner resume → final response.

The generated example uses the in-memory engine, so no Temporal is needed for development.

## 5) Evaluate your agent

The `Suite` in the design generates a typed evaluation harness under
`gen/evals/chat_quality`. Each scenario declares an observation type and an
exact check. Generated `Hooks` capture those observations; generated `Checks`
assess the saved values. `New` validates live inputs, while `ForAssessment`
constructs an offline suite with no capture hooks or live dependencies.
`MustToolContract` exposes tool contracts reachable from the agent.
`goa example` scaffolds `cmd/chat_quality-evals` once; application edits survive
regeneration.

To reuse expectations in focused cases and complete flows, declare a named
`Component` over an observation type and apply it with `Assess`. Keep assertions
about the complete outcome beside those components. `Reasoning()` marks a
requirement that always needs the reasoning model; other requirements still
need reviewed qualification before native predictions can bypass reasoning.
See the [component example](../docs/evals.md#reuse-an-assessment-in-focused-tests-and-complete-flows)
and [qualification contract](../docs/evals.md#qualify-automatic-decisions).

The `greeting_reply` hook in [main.go](cmd/chat_quality-evals/main.go) subscribes
an `evidence.Collector` to the runtime's events while the chat agent runs on
the in-memory engine. [observations.go](cmd/chat_quality-evals/observations.go)
copies the question, answer, tool arguments and results, failures, and completion
state into `GreetingObservation`. Failed and partial product outcomes remain
data that checks can assess.

During assessment, `CheckGreetingReplyRunTools` reconstructs those saved facts
and runs `evidence.Expect` with the generated `helpers.AnswerTool()` descriptor.
It checks the original question, a nonempty tool result, and successful
completion. The default matching permits a failed attempt followed by a
successful retry. Typed predicates remain compile-checked against the tool's
payload and result fields.

```bash
go run ./cmd/chat_quality-evals              # whole suite
go run ./cmd/chat_quality-evals --tag smoke  # by tag
go run ./cmd/chat_quality-evals --scenario greeting_reply
go run ./cmd/chat_quality-evals --capture capture.json
go run ./cmd/chat_quality-evals --assess capture.json
```

`--capture` creates a new private archive without calling predicates or
evaluation models. It does not overwrite an existing file. `--assess` evaluates
the archive without running the agent. The full-run and assessment commands
print a JSON report and exit nonzero when anything fails. A shortened report
looks like:

```json
{
  "suite_id": "chat_quality",
  "scenarios": [
    {"id": "greeting_reply", "checks": [{"name": "run_tools", "passed": true}], "passed": true},
    {"id": "helpers_contract", "checks": [{"name": "payload_schema", "passed": true}], "passed": true}
  ],
  "passed": true
}
```

The full report also records archive and assessment identities, policy,
provenance, and separate capture and assessment durations. `Runner.Run` returns
`(Archive, Report, error)`; check the error and `report.Passed`.

This suite uses only exact checks, so the runner takes a nil assessment engine.
For semantic checks, declare `Requirement`, `Subject`, and `Evidence` in the
design. Construct a judge with `judge.New(modelClient, maxOutputTokens)`, pass it
to `eval.NewReasoningEngine`, and handle both constructor errors before creating
the runner. A selective engine can later accept System One predictions qualified
on reviewed examples and send other cases to reasoning. Saved observations let
you compare these approaches without rerunning the product. See the
[evaluation guide](../docs/evals.md) for qualification, comparison, and migration.

## 6) (Optional) Connect to Temporal for production

For production, start Temporal and configure the runtime:

```bash
# Start Temporal dev server
docker run --rm -d --name temporal-dev -p 7233:7233 temporalio/auto-setup:latest
```

Then modify the bootstrap to use the Temporal engine:

```go
import (
    "goa.design/goa-ai/runtime/agent/engine/temporal"
    "go.temporal.io/sdk/client"
)

eng, err := temporal.NewWorker(temporal.Options{
    ClientOptions: &client.Options{
        HostPort:      "127.0.0.1:7233",
        Namespace:     "default",
    },
    WorkerOptions: temporal.WorkerOptions{
        TaskQueue: "<service>_<agent>_workflow",
    },
})
if err != nil {
    log.Fatal(err)
}
rt := agentsruntime.New(runtimeStore, agentsruntime.WithEngine(eng))
```

The engine always installs Goa-AI's strict data converter and limits one
workflow or activity call to 1 MiB. Supply only connection and namespace
settings through `ClientOptions`; persist larger tool results first and return
their durable reference.

## 7) Customize the planner

The planner in `internal/agents/chat/planner/planner.go` already demonstrates
both planner decisions deterministically: `PlanStart` returns tool calls
(encoding the payload with `helpers.AnswerTool().Payload`), and `PlanResume`
reads the typed result decoded by the bootstrap and finalizes. To make the agent smart,
replace the deterministic decisions with LLM calls:

```go
func (p *chatPlanner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
    // 1. Get LLM client from runtime
    // mc, _ := in.Agent.PlannerModelClient("openai")

    // 2. Build the prompt from in.Messages. The runtime applies history
    //    policy when you send the model request.

    // 3. Let the model decide: return ToolCalls or a FinalResponse
    return &planner.PlanResult{
        FinalResponse: &planner.FinalResponse{
            Message: &model.Message{
                Role:  model.ConversationRoleAssistant,
                Parts: []model.Part{model.TextPart{Text: "Your response here"}},
            },
        },
    }, nil
}
```

## (Optional) Expose tools over MCP

This quickstart runs in process. To expose service methods as MCP tools, declare
`MCP(...)`, a service-level JSON-RPC route, and the methods to expose in your
design. Follow the [MCP server guide](../docs/dsl.md#mcp-server-definition) for
the complete design and generated adapters.

## Notes

- Always change design in `design/*.go` then run `goa gen` (and `goa example` as needed). Never edit `gen/` by hand.
- Service-owned tool specs and typed codecs live under `gen/<service>/toolsets/<toolset>/`.
  Agent exports use `gen/<service>/agents/<agent>/exports/<toolset>/`.
- Tool payload examples come from authored top-level Goa `Example(...)` blocks.
  Generated specs keep both schema variants plus parsed example input so
  provider adapters can choose schema annotations or native `input_examples`.
- Policies and caps are enforced by the runtime during execution; keep planners small and declarative.


## Try native tool search

The helper tool is declared `Deferred()` in this quickstart's design. The
regular command still uses its deterministic planner and needs no credentials.
The optional model-backed command uses that same generated contract:

```bash
export OPENAI_API_KEY=...        # Supply through your normal secret handling.
go run ./cmd/tool-search -provider openai -model YOUR_MODEL_ID

# Or use a Claude model supporting hosted tool search:
export ANTHROPIC_API_KEY=...
go run ./cmd/tool-search -provider anthropic -model YOUR_MODEL_ID
```

This makes billable model calls. It asks the model to discover the helper,
execute it, and report its fixed Tokyo answer. The command uses a two-minute
active run budget and a 4096-token output budget per logical model invocation;
search rounds share that output budget. These are example settings.

`Deferred()` and search word counts come from code generation. The planner
passes `AdvertisedToolDefinitions()` and existing messages to the registered
model client. OpenAI search executes inside its adapter; Claude search executes
at the provider. The planner handles only ordinary tool calls and final text.
The helper is intentionally a fixture and is not a general question-answering
service. A one-tool demo establishes wiring, not a token-savings benchmark.

For changing provider catalogs, consume `Toolset(FromRegistry(...))` or
`Use(registry)` and connect the clients with `rt.RegisterRegistry`. Providers
send generated `ToolSchemas()` during startup registration, then supply the
required duration-only `Registration.Renew` callback to renew that exact lease
without uploading definitions again. An active provider stops on lost lease
authority instead of registering again. See
[Tool search and dynamic registries](../docs/tool_search.md) for the complete
consumer/provider wiring, execution guarantees, supported endpoints, and
upgrade steps. No registry is required for the static example above.

An existing registry needs the [offline storage upgrade](../docs/runtime.md#registry-storage-upgrade)
before using this layout. Stop all old writers and preserve the current
catalog, retired-token history, calls, streams, and their expiry during
conversion. The wire version and schema fingerprints remain unchanged.
The preview guide describes the required verification; a published converter
is not yet supplied.
