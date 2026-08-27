# Goa‑AI Quickstart

Minimal, copy‑paste runnable example to go from zero → talking agent. Keep your design in `design/`, never edit `gen/`.

## Prerequisites

- Go 1.24+
- Goa v3 CLI (`go install goa.design/goa/v3/cmd/goa@latest`)

## 1) Scaffold a fresh project

```
mkdir -p $GOPATH/src/example.com/quickstart && cd $_
go mod init example.com/quickstart
go get goa.design/goa/v3@latest
go get goa.design/goa-ai@latest
```

## 2) Add a tiny design (design/design.go)

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
                Tags("smoke")
            })
            Scenario("helpers_contract", func() {
                Description("The helpers.answer tool contract is reachable from the agent.")
                Tags("contract")
            })
        })
    })
})
```

## 3) Generate code and example

```bash
goa gen example.com/quickstart/design
goa example example.com/quickstart/design
```

This creates:
- **`gen/`** - Generated code (never edit by hand), including one typed
  descriptor factory per tool (`helpers.AnswerTool()`) pairing the tool identifier with
  its payload and result codecs
- **`cmd/orchestrator/main.go`** - Runnable example using the bootstrap
- **`internal/agents/bootstrap/bootstrap.go`** - Wires runtime, registers agents and toolset executors
- **`internal/agents/chat/planner/planner.go`** - Application-owned planner (edit to connect your LLM)
- **`gen/<service>/completions/`** - Generated typed direct-completion helpers
- **`gen/evals/chat_quality/`** - Typed evaluation harness (hooks interface, validated inputs, tool contracts)
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
`gen/evals/chat_quality`: one hook method per scenario, validated typed inputs,
and `MustToolContract`, a lookup over the tool contracts reachable from the
agent. `goa example` scaffolds `cmd/chat_quality-evals` once; the hook bodies
are yours and survive regeneration.

This quickstart's `greeting_reply` hook demonstrates the framework evidence
flow (see `cmd/chat_quality-evals/main.go`): it bridges the runtime's event
bus into an `evidence.Collector` (package `goa.design/goa-ai/eval/evidence`)
while the real chat agent runs on the in-memory engine, then grades the run
with declarative expectations built from the generated typed tool descriptor:

```go
expect := evidence.Expect{
    Tools: []evidence.Tool{
        evidence.ExpectCall(genhelpers.AnswerTool(),
            func(p *genhelpers.AnswerPayload) error { /* assert arguments */ return nil },
            func(r *genhelpers.AnswerResult) error { /* assert result */ return nil },
        ),
    },
}
return eval.Result{Checks: expect.Checks(ev), Output: ev.Answer}, nil
```

The descriptor fixes the tool-name-to-codec pairing at generation time, so
the predicates are compile-checked against the tool's actual payload and
result types — a design change that renames or retypes a field breaks the
suite at compile time instead of silently never matching. `Expect` grades the
causal trajectory, failure classifications (`evidence.ExpectFailure`),
pending confirmations (`evidence.ExpectConfirmation`), forbidden tools, and
the terminal workflow phase.

```bash
go run ./cmd/chat_quality-evals              # whole suite
go run ./cmd/chat_quality-evals --tag smoke  # by tag
go run ./cmd/chat_quality-evals --scenario greeting_reply
```

The command prints a JSON report (scenarios in declaration order, per-check
outcomes) and exits non-zero when anything fails:

```json
{
  "suite_id": "chat_quality",
  "scenarios": [
    {"id": "greeting_reply", "result": {"checks": [{"name": "trajectory", "passed": true}, {"name": "terminal", "passed": true}], "output": "Deterministic demo answer to: What is the capital of Japan?"}, "passed": true},
    {"id": "helpers_contract", "result": {"checks": [{"name": "answer_payload_schema_present", "passed": true}]}, "passed": true}
  ],
  "passed": true
}
```

This suite is deterministic, so the runner takes a nil judge. When your hooks
return semantic `eval.Claims` about model output, pass an `eval.Judge` instead
— `eval/judge.New(modelClient)` wraps any `model.Client` in a strict,
calibration-checked LLM judge. See `docs/evals.md` in the goa-ai repository
for the complete contract.

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
rt := agentsruntime.New(agentsruntime.WithEngine(eng))
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

    // 2. Build prompt from in.Messages

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

## (Optional) HTTP / JSON‑RPC server

`goa example` also generated an HTTP JSON‑RPC server under `cmd/orchestrator`.

- Start it: `go run ./cmd/orchestrator -debug`
- It mounts the MCP‑compatible JSON‑RPC API on POST `/rpc`.
- Try a simple RPC (replace the tool name with one from your design):

```bash
curl -s http://localhost:8080/rpc \
  -H 'Content-Type: application/json' \
  -d '{
        "jsonrpc":"2.0",
        "id":1,
        "method":"tools/call",
        "params":{
          "name":"orchestrator.helpers.answer",
          "arguments": {"question": "What is the capital of Japan?"}
        }
      }' | jq .
```

Note: tool execution requires wiring executors. For a first run, the in‑process demo above is the simplest path. When you bind tools to service methods (`BindTo` in the design), `goa example` will scaffold executors you can fill in.

## Notes

- Always change design in `design/*.go` then run `goa gen` (and `goa example` as needed). Never edit `gen/` by hand.
- Service-owned tool specs and typed codecs live under `gen/<service>/toolsets/<toolset>/`.
  Agent exports use `gen/<service>/agents/<agent>/exports/<toolset>/`.
- Tool payload examples come from authored top-level Goa `Example(...)` blocks.
  Generated specs keep both schema variants plus parsed example input so
  provider adapters can choose schema annotations or native `input_examples`.
- Policies and caps are enforced by the runtime during execution; keep planners small and declarative.
