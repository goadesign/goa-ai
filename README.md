<p align="center">
  <a href="https://goa.design">
    <img alt="Goa-AI" src="https://raw.githubusercontent.com/goadesign/goa-ai/main/docs/img/goa-ai-banner.png" width="50%">
  </a>
</p>

<p align="center">
  <a href="https://github.com/goadesign/goa-ai/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/goadesign/goa-ai?style=for-the-badge"></a>
  <a href="https://goa.design/docs/2-goa-ai/"><img alt="Documentation" src="https://img.shields.io/badge/docs-goa.design-blue.svg?style=for-the-badge"></a>
  <a href="https://pkg.go.dev/goa.design/goa-ai"><img alt="Go Doc" src="https://img.shields.io/badge/godoc-reference-blue.svg?style=for-the-badge"></a>
  <a href="https://github.com/goadesign/goa-ai/actions/workflows/ci.yml"><img alt="GitHub Action: CI" src="https://img.shields.io/github/actions/workflow/status/goadesign/goa-ai/ci.yml?branch=main&style=for-the-badge"></a>
  <a href="https://goreportcard.com/report/goa.design/goa-ai"><img alt="Go Report Card" src="https://goreportcard.com/badge/goa.design/goa-ai?style=for-the-badge"></a>
  <a href="LICENSE"><img alt="Software License" src="https://img.shields.io/badge/license-MIT-brightgreen.svg?style=for-the-badge"></a>
</p>

<h1 align="center">Design-First Agentic Systems in Go</h1>

<p align="center">
  <b>Declare agents, tools, MCP servers, policies, and structured model output in Goa. Generate the plumbing. Run it durably.</b>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> |
  <a href="#what-you-can-build">What You Can Build</a> |
  <a href="#how-it-works">How It Works</a> |
  <a href="#production">Production</a> |
  <a href="#learn-more">Learn More</a>
</p>

---

## Why Goa-AI

Most agent frameworks start with code and ask you to keep the contracts in your head: JSON schemas, tool names, retry behavior, model output formats, UI events, and workflow state. Goa-AI starts with the contract.

You describe the agent system in the same design-first style as Goa services. `goa gen` turns that design into typed Go packages: tool specs, JSON schemas, codecs, workflow registrations, clients, MCP adapters, registry clients, and structured completion helpers. The runtime then executes the generated contracts with policy enforcement, streaming, replayable run logs, and an engine you can swap from in-memory development to Temporal-backed production.

| If you care about... | Goa-AI gives you... |
| --- | --- |
| Strong tool contracts | Goa types, validations, examples, generated JSON Schema, and schema-first input checks that stay intact across raw model gateways |
| Durable agent execution | Plan/execute workflows with retries, budgets, cancellation, typed input checkpoints, and Temporal support |
| Existing service logic | `BindTo` and generated transforms that connect tools to Goa service methods |
| Structured final answers | Service-owned `Completion(...)` contracts with unary and streaming helpers |
| Repeatable agent checks | Generated evaluation hooks, exact scenario selection, bounded concurrency, and calibrated semantic judging |
| Multi-agent systems | First-class agent-as-tool composition with child runs and linked streams |
| Human approval | Await/clarification flows plus design-time and runtime tool confirmation |
| Real-time UI | Provisional assistant text and thinking sent as each validated fragment arrives, with explicit acceptance or removal and canonical transcript persistence only after planner selection |
| External tools | MCP callers, generated MCP servers, external MCP schemas, and token-fenced registry routing with incarnation leases plus catalog-owned health epochs |
| Production operations | Host-owned runtime storage, Mongo-backed memory and prompt stores, Pulse streaming, model clients, telemetry hooks |

Goa-AI is not a prompt wrapper. It is a contract and runtime layer for agentic Go services.

Registry-routed providers use deterministic admission-generation tokens derived
from the wire protocol version, canonical schema identity, and a
deployment-issued admission revision, with lease-derived renewal, exact
drain-then-close-intake lifecycle release, and token-fenced calls, deltas, and
results. The registry
owns graceful admission handoff: same-token replicas
scale or roll together. During a different-token rolling deployment, the new
provider retries registration while the old admission drains. New calls wait
without being published until the replacement is healthy, using that wait as
part of their existing execution deadline. `Serve` generates one UUID incarnation, so
a delayed release from an old process cannot delete its replacement. Lease
membership, health epoch, and last pong live in one CAS catalog record. Every
retirement and replacement permanently retains the prior token; this set grows
with distinct admissions and cannot be truncated safely. The read-only
`CheckAdmission` operation derives its result from that same record so
deployment systems can verify that the exact registration token derived from
generated schemas and an admission revision has a routable lease and fresh pong
without making workload readiness depend on admission. Generated toolset specs
expose `RegistrationToken(admissionRevision)` so deployment code does not
reimplement schema fingerprinting.
Providers send that generated fingerprint with registration. The registry
independently derives a fingerprint from the submitted toolset and rejects a
mismatch before creating a stream or admission.
The gateway derives a global transport `ToolUseID` from required run plus call
identity. Its global
call record stores a token-independent request digest, the provider token that
becomes immutable at publication, overload state, and the complete canonical terminal. Exact retained calls
replay before current routing or health lookup after a generation changes.
Transport readers remain independent and oldest-first. Queue saturation emits
top-level retry control with reason `provider_overloaded`, bounded delay, and no
planner failure. The executor requests republication through the distinct
`RetryTool` operation using the original admission token; the registry refuses
to bind retry to a replacement provider. One call record owns an absolute
execution deadline no longer than `MaxToolCallWait`, a later bounded retention
expiration, and terminal state. Provider handler contexts and executor waiting
use the execution deadline; result streams use the retention expiration.
The registry atomically stores each terminal with the call record. Replay
restores a trimmed terminal from bounded delivery history. Output deltas have
byte and per-call count limits, and overload reporting is idempotent per request
event. Retired and draining leases retain authority to settle only claims that
committed before draining. A claim-operation ID lets transport retries recover
the original execute decision while a later event redelivery remains
non-executable. At execution deadline, or sooner if
that lease disappears, registry-owned settlement publishes `outcome_unknown`
because the effect may have occurred; execution never transfers to another
provider and the canonical terminal remains retained.
Registry startup strictly validates every authoritative catalog record and
fails before serving if any persisted value uses an incompatible shape. An
active toolset with no healthy provider waits until its execution deadline.
Publication then atomically rechecks the selected provider. If that provider
started draining, the still-unpublished call waits for and selects the current
healthy provider without extending its deadline. The provider assignment
becomes immutable when request publication commits. If the deadline expires
first, the registry commits the rejected state before returning typed
`call_not_admitted`. This covers a release handoff without guessing whether the
absence came from a deployment or an outage. Exact
retries cannot execute that identity while the run-scoped decision is retained,
so executors may safely replan; only published calls with ambiguous execution
become `outcome_unknown`.
This decision contract is wire protocol version 9. Registry replicas and
retained catalog records must use that exact protocol; do not overlap registry
versions or infer how to translate unknown records. Upgrading from protocol 8
requires the catalog-only, forward-only rebootstrap described in the
[preview upgrade guide](docs/runtime.md#preview-upgrade-guide); retained call
records and Pulse streams are not reset.
`Serve` also exposes the canonical ToolUseID through context for durable method
deduplication without changing tool payloads. Workers recheck the
absolute deadline when dispatching local backlog and acknowledge expired calls
only after the registry authenticates the provider lease and confirms
expiration using Redis time. Request-stream
retention trims only below every consumer group's earliest pending ID; raw
length trimming never removes pending calls. `Unregister` is reserved for
retirement. See
[Runtime: Registry-Routed Provider Execution](docs/runtime.md#registry-routed-provider-execution-service-side)
for the complete contract.

---

## Quick Start

This path gives you a generated, runnable agent and a typed direct-completion helper. The generated example uses the in-memory engine, so there are no external services required.

### 1. Create a Module

```bash
go install goa.design/goa/v3/cmd/goa@v3.31.0-preview.3

mkdir quickstart && cd quickstart
go mod init example.com/quickstart
go get goa.design/goa/v3@v3.31.0-preview.3 goa.design/goa-ai@latest
mkdir design
```

### 2. Add `design/design.go`

```go
package design

import (
	. "goa.design/goa/v3/dsl"
	. "goa.design/goa-ai/dsl"
)

var _ = API("orchestrator", func() {})

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

var TaskDraft = Type("TaskDraft", func() {
	Attribute("name", String, "Task name")
	Attribute("goal", String, "Outcome-style goal")
	Required("name", "goal")
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
	})
})
```

### 3. Generate and Run

```bash
goa gen example.com/quickstart/design
goa example example.com/quickstart/design
go run ./cmd/orchestrator
```

Expected shape:

```text
RunID: orchestrator-chat-...
Assistant: Tool helpers.answer returned {"text":"Tokyo is the capital of Japan."}
Completion draft_task: ...
Completion delta draft_task: ...
Completion stream draft_task: ...
```

Generation creates application-owned scaffolding under `internal/agents/` and generated contract code under `gen/`. Edit the planner and bootstrap files; do not edit `gen/`.

### 4. Run an Agent from Application Code

The generated agent package exposes a typed client. Sessionful runs require an explicit session; one-shot runs do not.

```go
runtimeStore := storageinmem.New()
if _, err := runtimeStore.CreateSession(ctx, "session-1", time.Now().UTC()); err != nil {
	log.Fatal(err)
}

rt, cleanup, err := bootstrap.New(ctx, runtimeStore)
if err != nil {
	log.Fatal(err)
}
defer cleanup()

client := chat.NewClient(rt)
out, err := client.Run(ctx, "session-1", []*model.Message{{
	Role:  model.ConversationRoleUser,
	Parts: []model.Part{model.TextPart{Text: "Hello"}},
}}, runtime.WithRunID("run-1"))
if err != nil {
	log.Fatal(err)
}
fmt.Println(out.RunID)

// For request/response work that should not belong to a session:
out, err = client.OneShotRun(ctx, []*model.Message{{
	Role:  model.ConversationRoleUser,
	Parts: []model.Part{model.TextPart{Text: "Summarize this file"}},
}})
```

### 5. Replace the Stub Planner

Planners decide what happens next: final response, tool calls, await human input,
or terminal tool result. By default, a run that reaches a time or call limit
loads saved messages and asks the planner to finish. Applications whose terminal
result is a fixed structured record may instead supply `LimitTerminalPlans`:
one payload-only terminal call for each configured limit. The runtime validates
all three calls before planning, then executes the matching `TerminalRun()` tool
without loading saved messages. The individual `tool_failure` termination case
always uses saved messages because its final response may depend on the failed
result. A `correct_call` failure keeps the failed tool available and supplies
its rejected input and generated validation issues to the next planner turn.
The planner may retry one or more calls, combine work, use another advertised
tool, ask for input, or finish from evidence already collected. Caller
`WithRestrictToTool` policy remains run-scoped and still applies to every tool.
For side-effect-owned operations, `WithRunCompletionTool` instead requires one
declared non-terminal tool to succeed; planner text and limit finalization
cannot substitute for that success.
Tool executors decide how work is performed.

```go
func (p *Planner) PlanStart(ctx context.Context, in *planner.PlanInput) (*planner.PlanResult, error) {
	mc, ok := in.Agent.PlannerModelClient("default")
	if !ok {
		return nil, errors.New("model client default is not registered")
	}

	summary, err := mc.Stream(ctx, &model.Request{
		Messages: in.Messages,
		Tools:    in.Agent.AdvertisedToolDefinitions(),
		Stream:   true,
	})
	if err != nil {
		return nil, err
	}
	if len(summary.ToolCalls) > 0 {
		return &planner.PlanResult{ToolCalls: summary.ToolCalls}, nil
	}
	return &planner.PlanResult{
		FinalResponse: summary.FinalResponse(),
	}, nil
}
```

Register model clients during bootstrap with `rt.RegisterModel(...)` or runtime
factories such as `rt.NewOpenAIModelClient(...)`, `rt.NewBedrockModelClient(...)`,
`rt.NewVertexGeminiModelClient(...)`, and `rt.NewVertexAnthropicModelClient(...)`.
Provider adapters now separate raw transport work from consumer validation.
`openai.New`, `anthropic.New`, `bedrock.New`, `bedrock.NewAnthropic`,
`vertex.New`, and
`gateway.NewRemoteClient` return an opaque validated `model.Client`. Use the
matching `NewProvider` constructor for provider-side middleware, gateways, or
other code that deliberately operates before canonical output validation. A
raw `model.Provider` remains directly callable and may return unvalidated
responses or streams. Pass it to `model.NewClient` before using an API that
requires canonical model output. External packages cannot implement a valid
`model.Client`; APIs that accept one verify the package-owned opaque client
before inference.

Mechanical response rejections return `*model.OutputValidationError`.
`Kind()` reports one closed, privacy-safe category such as `tool_arguments` or
`stream_protocol`; it never contains response text, provider text, tool names,
arguments, or schema paths. The category is diagnostic only. Recovery still
requires exact correction guidance produced by typed input validation or
planner policy, and remains bounded by the runtime's configured recovery-turn
limit.

Use `bedrock.NewAnthropic` for Claude deployments on Amazon Bedrock. It sends
Anthropic Messages requests through Bedrock `InvokeModel`, so authored tool
examples, forced tool choice, thinking, and prompt caching keep one
representation on initial and resumed turns. User-message `ImagePart` values
are sent as Anthropic base64 image blocks for PNG, JPEG, GIF, and WebP content.
For models such as Sonnet 5 and Opus 5 that accept forced tools but reject
`output_config.format`, structured output uses one private forced tool and is
returned to callers as the same canonical completion. Its required exact
counter receives that same effective request with the Bedrock inference-profile
prefix removed for a compatible counting endpoint such as Bedrock Mantle.
`bedrock.New` remains the Converse adapter for other Bedrock models and existing
Converse integrations.

Remote transports that expose exact token counting use
`gateway.NewCountingRemoteClient`; it forwards a separate count operation in
addition to completion and streaming. `gateway.NewRemoteClient` deliberately
has no counting capability and returns `model.ErrTokenCountingUnsupported`
instead of estimating.

---

## How It Works

```text
design/*.go
  Agents, toolsets, completions, policies, MCP, registries
      |
      | goa gen
      v
gen/
  Agent packages, tool specs, codecs, schemas, workflow registrations,
  typed clients, completion helpers, MCP adapters, registry clients
      |
      | runtime.New(...)
      v
Runtime
  Plan -> execute tools -> resume -> finish
  Policy, memory, streaming, run log, telemetry, engine integration
      |
      +-- in-memory engine for development
      +-- Temporal engine for durable production workers
```

The key separation is deliberate:

- The **DSL** owns contracts: names, schemas, validations, examples, tags, policies, confirmation, MCP exposure, and registry sources.
- **Generated code** owns repetitive infrastructure: JSON codecs, JSON Schema, route metadata, workflow/activity registrations, client helpers, completion helpers, and transforms.
- The **runtime** owns execution: planner calls, tool admission, policy checks, tool activities, child workflows, awaits, streaming, memory, run logs, and telemetry.
- **Your code** owns judgment and side effects: planners, service methods, tool executors, model choice, storage, deployment, UI, and product policy.

---

## What You Can Build

### Typed Tools and Toolsets

Toolsets are callable capabilities. They can be inline, service-backed, MCP-backed, registry-backed, or implemented by another agent.

```go
var Docs = Toolset("docs", func() {
	Description("Document retrieval tools")
	Tags("docs", "read")

	Tool("search", "Search indexed documents", func() {
		Args(func() {
			Attribute("query", String, "Search phrase", func() {
				MinLength(1)
				MaxLength(500)
			})
			Attribute("limit", Int, "Maximum results", func() {
				Minimum(1)
				Maximum(50)
				Default(10)
			})
			Required("query")
		})
		Return(ArrayOf(Document))
		BoundedResult()
		CallHintTemplate("Searching docs for {{ .Query }}")
	})
})
```

**What you get:**
- JSON Schema for LLM function calling (auto-generated)
- Validation at boundaries: tool arguments are always JSON objects, and the
  advertised schema is enforced before any attached input decoder. Only
  schema rejections and typed tool-input validation errors get limited-size
  correction guidance that omits rejected arguments; ordinary decoder and
  internal errors stop the run. See the
  [runtime tool-input contract](docs/runtime.md#model-visible-tool-arguments).
  Streaming tool argument fragments and completed calls remain withheld until
  the complete provider response matches the stream; the runtime can then
  schedule one replacement planning activity while retaining final usage and
  without retaining or executing the rejected arguments.
  Incomplete provider streams remain terminal.
- Timeout and parent-budget failures are terminal for the current run and use
  `finish` recovery. Planners may repair invalid arguments, but elapsed
  execution time is not an instruction to repeat a call.
- Type-safe Go structs for payloads and results
- Provider-facing examples only when you author a top-level Goa `Example(...)`
  on the tool payload. Codegen removes synthesized placeholder examples from
  the complete schema graph, then precomputes the annotated schema, the schema
  with the authored root `example` removed, and the parsed example input so
  OpenAI-style providers and Claude through `bedrock.NewAnthropic` consume
  schema annotations while direct Anthropic and Claude-on-Vertex receive
  provider-native `input_examples` under the required tool-examples contract,
  including exact Anthropic token counting.
- Explicit control-plane contracts: `Bookkeeping()` keeps calls durable and
  model-visible while exempting them from tool-call and recovery-turn budgets and omitting
  successful results from typed future `ToolOutputs`

### Bind Tools to Goa Services

Use `BindTo` when the best tool implementation is already a service method. Use `Inject` for infrastructure fields that should not be model-visible.

```go
Method("search_documents", func() {
	Payload(func() {
		Attribute("query", String, "Search phrase")
		Attribute("session_id", String, "Current session")
		Required("query", "session_id")
	})
	Result(ArrayOf(Document))
})

Agent("chat", "Document assistant", func() {
	Use("docs", func() {
		Tool("search", "Search documents", func() {
			Args(func() {
				Attribute("query", String, "Search phrase")
				Required("query")
			})
			Return(ArrayOf(Document))
			BindTo("search_documents")
			Inject("session_id")
		})
	})
})
```

The generator emits typed transforms where shapes are compatible. `Inject` names that match a `runtime.ToolCallMeta` field (`run_id`, `session_id`, `turn_id`, `tool_call_id`, `parent_tool_call_id`) are meta-backed; any other name is label-backed, read from labels supplied via `runtime.WithLabels(...)` at run start. Both sources run the injected field's Goa `String` validation before assignment. Named Goa `String` types are supported, while custom `struct:field:type` replacements are rejected. See [`docs/dsl.md`](docs/dsl.md) and [`docs/runtime.md`](docs/runtime.md) for the full contract.

### Structured Direct Completions

Use `Completion(...)` when the model should return a typed value directly instead of calling a tool.

```go
var Draft = Type("Draft", func() {
	Attribute("name", String, "Task name")
	Attribute("goal", String, "Outcome-style goal")
	Example(map[string]any{
		"name": "Investigate startup alarms",
		"goal": "Explain every alarm observed during startup.",
	})
	Required("name", "goal")
})

var _ = Service("tasks", func() {
	Completion("draft_from_transcript", "Produce a task draft directly", func() {
		Return(Draft)
	})
})
```

`goa gen` emits `gen/<service>/completions/` with schemas, authored examples,
codecs, `Complete<Name>(...)`, and typed `StreamComplete<Name>(...)` helpers.
The codec-bearing `completion.Spec` remains private to those wrappers. When a
caller genuinely needs the authored example, `<Name>Example()` returns an
isolated raw JSON copy. Completion names are part of the contract: 1-64 ASCII
characters, letters/digits/`_`/`-`, starting with a letter or digit.

Unary helpers install their generated decoder before provider work, request
provider-enforced structured output, and decode with generated codecs. A
low-level `model.Request` may use `StructuredOutput` without a local decoder;
it must set a nonempty `StructuredOutput.Name` and a nonempty, compilable
`StructuredOutput.Schema`. Shared request validation rejects either omission or
an invalid schema before provider work. Before copying or exposing a request,
the validated client applies one 16 MiB and 100,000-value budget across
messages, media, tool contracts, and structured-output schemas. The validated
client then enforces one canonical completion envelope and validates its final
JSON against the request schema. Provider-native enforcement remains an earlier
optimization, not the authoritative acceptance check. Typed completion helpers
additionally guarantee exact generated decoding. When the return type has an
authored root `Example(...)`, adapters forward its canonical JSON through
provider-native example fields where available. Each helper makes one provider
request. If the provider returns JSON that the generated codec rejects, the
helper returns a non-retryable `planner.OutputContractError` and does not ask the
model again.
`completion.Response.ModelResponse` contains that exact model response.
Streaming providers may expose preview `completion_delta` chunks and never
restart after emitting them. Even the low-level validated stream retains the
final `completion` chunk and every later chunk until the provider ends normally,
both final JSON representations satisfy the request schema, and their bytes
match exactly, including surrounding whitespace. A caller-supplied completion
validator adds checks and cannot replace those framework checks. The typed
helper then decodes and exposes the value. Providers that cannot preserve the
structured-output contract fail explicitly with
`model.ErrStructuredOutputUnsupported`.
The Bedrock Converse adapter uses one private strict tool for Claude 4.6 and a
private non-strict tool for models that expose forced tools before native
`OutputConfig`. `bedrock.NewAnthropic` applies the same provider-neutral
contract before `InvokeModel`: Sonnet 5 and Opus 5 receive one forced private
tool because Bedrock Messages rejects both `output_config.format` and `strict`.
The adapter removes the private object wrapper and exposes one canonical
completion; its exact counter receives the same tool definition and choice.

When updating generated completion callers, replace direct `Spec<Name>` access
with `Complete<Name>(...)` or `StreamComplete<Name>(...)`. Use
`<Name>Example()` only when the caller needs the authored example; generated
codecs and schemas are owned by the wrappers.

### Agent-as-Tool Composition

Agents can export toolsets that other agents use. The nested agent runs as a
child workflow, not as flattened helper code. The parent finish-by timer covers
child execution; when it expires, the runtime cancels pending child workflows
before returning terminal tool results to the parent planner.

```go
Agent("researcher", "Research specialist", func() {
	Export("research", func() {
		Tool("deep_search", "Perform deep research", func() {
			Args(ResearchRequest)
			Return(ResearchReport)
		})
	})
})

Agent("coordinator", "Delegates specialist work", func() {
	Use(AgentToolset("orchestrator", "researcher", "research"))
})
```

Parent runs receive a tool result with a child run link. The engine first
accepts the child workflow under its stable, single-use run ID. A second
explicit child start with that ID is rejected even after the first child
finishes; deterministic Temporal replay remains part of the original start.
Temporal explicitly terminates the child if its parent workflow closes first.
The child's first write atomically creates its session metadata and appends
`ChildRunLinked` followed by `RunStarted`. If session ending wins that race, it
also appends the canceled `RunCompleted` record; the link and exact start
identity remain visible, and no planner or tool work begins. The link stores only additional
child labels beyond its dedicated parent, tool, session, child-run, and
child-agent fields. Streams emit `child_run_linked` so UIs can render nested
runs without losing identity, logs, or telemetry.
If a child asks for external input, the parent workflow ends with the same
visible request. Continuing the parent starts a new child workflow from the
child checkpoint; the parent tool call stays open until that child finishes.

### Runtime Policies, Tags, and Timing

Policies are runtime-enforced, not planner suggestions.

```go
Agent("operator", "Production operations agent", func() {
	RunPolicy(func() {
		DefaultCaps(MaxToolCalls(20), MaxRecoveryTurns(3))
		Timing(func() {
			Budget("5m")
			Plan("45s")
			Tools("90s")
		})
		OnMissingFields("await_clarification")
		History(func() {
			KeepRecentTurns(20)
		})
		Cache(func() {
			AfterSystem()
			AfterTools()
		})
	})
})
```

Activity execution uses three distinct timeout bounds:

- `ScheduleToStartTimeout` limits how long each attempt may wait in the worker
  queue.
- `StartToCloseTimeout` limits one running attempt. The `Plan` and `Tools`
  timing values above configure this execution budget.
- `ScheduleToCloseTimeout` limits the total activity lifetime, including queue
  wait, every retry attempt, and retry backoff.

For planner calls, the runtime sets the total lifetime to the remaining run
deadline. Initial and resumed planning use the run budget; finalization uses
the separate hard deadline. If initial or resumed planning exhausts its total
lifetime, the runtime spends the reserved finalizer window on one explicit
finalization turn. Queue and attempt timeouts remain distinct failures.

History can also use model-assisted compression: declare
`CompressAtMaxInputTokens` or `CompressAtTurns` triggers plus `KeepMaxInputTokens`
or `KeepMaxTurns` exact-retention budgets inside `History`. Token budgets are
counted at runtime by a history model that implements `model.TokenCounter` with
exact counts and keep only whole recent turns, never truncated tool exchanges.

Per-run options can further restrict execution:

```go
out, err := client.Run(ctx, "session-1", messages,
	runtime.WithRunID("run-1"),
	runtime.WithRunTimeBudget(2*time.Minute),
	runtime.WithLimitTerminalPlans(runtime.LimitTerminalPlans{
		TimeBudget: runtime.LimitTerminalCall{
			Name: "jobs.complete",
			Payload: rawjson.Message(`{"outcome":"time_limit"}`),
		},
		ToolCallCap: runtime.LimitTerminalCall{
			Name: "jobs.complete",
			Payload: rawjson.Message(`{"outcome":"tool_limit"}`),
		},
		RecoveryCap: runtime.LimitTerminalCall{
			Name: "jobs.complete",
			Payload: rawjson.Message(`{"outcome":"recovery_limit"}`),
		},
	}),
)
```

Tool filters remain independent run options:

```go
out, err := client.Run(ctx, "session-2", messages,
	runtime.WithRunID("run-2"),
	runtime.WithRestrictToTool("docs.search"),
	runtime.WithRunCompletionTool("docs.search"),
	runtime.WithTagPolicyClauses([]runtime.TagPolicyClause{
		{AllowedAny: []string{"read", "safe"}},
		{DeniedAny: []string{"destructive"}},
	}),
)
```

`WithRunCompletionTool` is for operations whose success is the tool side effect,
not a later assistant response. The named tool must belong to the executing
agent, be budgeted, be non-terminal, and be allowed by the other run policies.
Its call must be the only action in that planner response: another call or an
await request is rejected. The run cannot request post-tool synthesis because
the resulting terminal planner answer cannot satisfy the completion policy. A
successful call ends the run immediately.
Correctable failures may retry within the normal caps. Planner text, forced
finalization, and exhausted caps or deadlines cannot substitute for the
required tool success; those paths fail the run. Do not combine this option with
`LimitTerminalPlans`, which assigns a different outcome to exhausted limits.

### External Input and Continuations

Each accepted user input starts one top-level workflow for that turn. The
workflow ends with either the turn's final result or an external-input
suspension. Nested agents still run as linked child workflows.

Clarifications, structured questions, external tool results, and confirmations
end the current workflow with `RunOutput.Suspension`. No workflow remains open
while a person is deciding. Before that workflow completes, the runtime stores
the suspension, suspended status, and matching record together under the
completed run ID. `LoadRunSuspension` therefore exposes only committed
suspensions. A workflow that is still running or paused returns
`runtime.ErrRunSuspensionNotReady`; callers should treat this as temporary
dependency availability. A completed, failed, or canceled run returns
`session.ErrRunSuspensionNotFound` because that terminal outcome can never
continue. A run recorded as suspended returns
`runtime.ErrRunSuspensionCorrupt` when its stored checkpoint is missing,
malformed, inconsistent with its stored ID, invalid, or belongs to another
predecessor run. This error identifies permanent stored-state corruption only;
runtime-store failures retain their original errors. The application first
calls `PrepareContinuation` with the completed run ID, the requested successor
ID, and one response to the first pending request. Preparation validates the
complete saved checkpoint and response against the current generated
definitions, copies the exact workflow input, and performs no write or workflow
start. A rejected response therefore leaves the requested run ID unused. The
application then atomically accepts that prepared answer, so concurrent
requests cannot continue the same state twice, and calls `StartPrepared`.
It may retry that same prepared value after an uncertain engine response:

```go
out, err := client.Run(ctx, "session-1", messages, runtime.WithRunID("run-1"))
if err != nil {
	return err
}
if out.Suspension != nil {
	pending := out.Suspension.Pending[0]
	prepared, err := client.PrepareContinuation(
		ctx,
		"session-1",
		out.RunID,
		"new-run-id",
		"new-turn-id",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID:     pending.Await.Clarification.ID,
			Answer: "Unit 7",
		}},
		runtime.WorkflowOptions{
			Memo: map[string]any{"account_id": "account-42"},
		},
	)
	if err != nil {
		return err
	}
	preparedBytes, err := prepared.MarshalBinary()
	if err != nil {
		return err
	}
	// This application-owned method uses one database transaction to accept the
	// answer and store the prepared workflow ID with preparedBytes.
	err = workflowStarts.AcceptContinuation(ctx, out.RunID, prepared.RunID(), preparedBytes)
	if err != nil {
		return err
	}
	// A later process loads the same bytes. No in-memory value from preparation
	// is required after the database transaction commits.
	preparedBytes, err = workflowStarts.Load(ctx, "new-run-id")
	if err != nil {
		return err
	}
	stored, err := runtime.ParsePreparedRun(preparedBytes)
	if err != nil {
		return err
	}
	handle, err := client.StartPrepared(ctx, stored)
	if err != nil {
		return err
	}
	out, err = handle.Wait(ctx)
}
```

Initial runs use the same durable command when an application must record the
exact request before launch. `Prepare` validates the run and returns a
`PreparedRun`. Its `RunID` method returns the exact workflow ID that the
application must store with the command. `MarshalBinary` creates the versioned
storage bytes, `ParsePreparedRun` loads them in a later process, and
`StartPrepared` submits the request. `Start` is the convenience form that
prepares and starts immediately. `Continue` does the same for a continuation
and waits for its result.

`Prepare` and `PrepareOneShot` are client-only operations: they copy and
validate the complete request without writing runtime storage, sealing worker
registration, or calling the workflow engine. `PrepareContinuation` additionally
reads the saved suspension. It still performs no write and does not start a
workflow. Preparation also does not create the optional storage bytes.
`MarshalBinary` is the storage boundary, while `StartPrepared` is the engine
submission boundary. `Start` and `Continue` do not call `MarshalBinary`.

Prepared bytes can contain the complete transcript, tool results, and a private
continuation checkpoint. Keep them in trusted, access-controlled application
storage. Memo, search attributes, and the selected task queue configure the
engine start; `api.RunInput` contains only workflow input. A workflow start may
use at most `engine.MaxPayloadBytes` (1,048,576) bytes in total. The stored JSON
record has a separate 8,388,608-byte limit for the complete encoded record.
That larger storage limit does not increase the workflow request limit. Store
large domain values separately and prepare a durable reference instead. See
[External Input and Workflow Continuations](docs/runtime.md#external-input-and-workflow-continuations)
for the exact values counted by each limit.

`MarshalBinary` reports a storage-encoding failure as
`ErrPreparedRunRejected`. The failure does not change the in-memory
`PreparedRun`, which remains valid for `StartPrepared`; an application that
requires durable admission must not start it until its bytes have been stored.
`ErrPreparedRunRejected` from `ParsePreparedRun` means the stored bytes are
malformed or are not the exact format produced by `MarshalBinary`; they cannot
be retried. `StartPrepared` returns the
same error when the request no longer satisfies the current generated agent
contract; that command cannot start with this generated release. It also
returns the error when valid bytes are passed to the wrong agent client. In
that case the bytes remain valid and must be submitted through the matching
generated client. `ErrWorkflowStartFailed` means the engine did not confirm the start.
Goa-AI does not retry the start automatically. The application explicitly calls
`StartPrepared` again with the same value, or parses and submits the same stored
bytes after a restart. A nested `engine.ErrWorkflowStartConflict` is permanent
because a different request already owns that workflow ID.

The checkpoint is opaque and may contain private planner state. The runtime
store keeps it; callers pass only the completed run ID and the user's
typed response. Callers may also select the task queue and attach memo or
search attributes to the new workflow; these options do not override the
checkpoint's planner policy or execution state. Continuation methods take these
settings as a `runtime.WorkflowOptions` value. Initial and one-shot runs use
`WithTaskQueue`, `WithMemo`, and `WithSearchAttributes` instead. Before routing
a continuation, the runtime verifies the checkpoint version, visible pending
requests, and required labels, planner results, nested child suspensions, and
generated tool contracts. The receiving worker checks the same immutable input
again before restoring it, so compatible
tool evolution continues while incompatible saved values fail at the codec
boundary. When an external answer completes a tool call from the previous
workflow, the emitted `tool_end` keeps the current result run as its event run
ID and carries the original call run in `call_run_id`. Stream consumers can
therefore pair the result with the exact `tool_start` without searching prior
runs.

The host application owns session creation, ending, and permanent deletion.
Its durable implementation stores sessions, runs, continuation checkpoints,
and ordered run records in one repository, and exposes the worker-facing
operations through `storage.Store`. When a separate Session service owns that
repository, runtime workers use a `storage.Store` adapter built on its generated
client instead of opening the database. This lets each workflow state change
and its matching record commit together. A purge first makes the session ID
permanently unavailable, then removes all runtime data for the ended session.
Goa-AI includes an integrated in-memory implementation for local development
and tests. Production hosts own their durable implementation, database schema,
migrations, and administrative API.

Generated agents, completion packages, runtime workers, and their callers form
one release unit and use one generated contract. New saved runs use
`goa-ai.run-suspension.v7`. Every successful tool with a result type stores the
complete JSON accepted by its generated result codec. Successful tools without
a result type and failed tools store no result JSON. Planner activities carry
run-record identifiers and load those exact bytes from the runtime store, so
workflow history stays small without creating a successful result whose value
is missing. Model-authored await items preserve the runtime
`ToolCallID` separately from the provider `ModelToolCallID`. Suspensions with
another shape fail at the typed checkpoint boundary.

Sensitive tools can require approval before execution:

```go
Tool("delete_record", "Delete a stored record", func() {
	Args(DeleteRecordRequest)
	Return(DeleteRecordResult)
	Confirmation(func() {
		Title("Confirm record deletion")
		PromptTemplate("Delete record {{ .RecordID }}?")
		DeniedResultTemplate(`{"status":"denied"}`)
	})
})
```

The first workflow emits an await-confirmation event and ends with a
confirmation suspension. A new workflow consumes an exact
`api.ConfirmationDecision`, records the durable authorization event, and only
then executes the tool. Denials produce schema-compliant tool results so
planners and transcripts remain deterministic.

### Bounded Results and Server Data

Large results need two views: a small model-facing view and rich server-side data for UIs or downstream systems.

```go
Tool("get_time_series", "Get a bounded time-series view", func() {
	Args(TimeSeriesRequest)
	Return(TimeSeriesSummary)
	BoundedResult(func() {
		ContinueWith("continue_time_series", "cursor")
		NextCursor("next_cursor")
	})
	ServerData("charts.points", TimeSeriesPoints, func() {
		Description("Chart points for observer-facing UI")
		AudienceInternal()
		FromMethodResultField("ChartPoints")
	})
	ServerDataDefault("off")
})

Tool("continue_time_series", "Continue a bounded time-series view", func() {
	Args(ContinuationRequest) // one required cursor field
	Return(TimeSeriesSummary)
	BoundedResult(func() {
		Cursor("cursor")
		NextCursor("next_cursor")
	})
})
```

`BoundedResult` makes truncation explicit through runtime-owned bounds metadata
(`returned`, `truncated`, `total`, `next_cursor`, and `refinement_hint`). `total`
may be required when the service always computes exact cardinality and optional
otherwise. Bounds metadata is success-only: error results never carry bounds.
Generated tool specs and result JSON use model-facing JSON names, so lower-camel
Goa fields such as `nextCursor` are exposed as `next_cursor`.

Use `ContinueWith` when the cursor already carries the resolved query: the
originating tool keeps an honest semantic payload, the required cursor-only
sibling resumes it, and the runtime tracks each unfinished query independently.
A page with zero returned items is advanced automatically because it contains
no evidence for a model to evaluate. After a page returns items with a next
cursor, the runtime advertises a temporary no-argument action describing the
original model-visible input. A truncated result without a cursor exposes its
refinement hint instead. The bounded-result reminder names that same temporary
action, so the model sees one truthful continuation name. The model can choose among parallel
result sets without copying a cursor or call ID. The runtime maps the chosen
action to the generated continuation tool, binds its exact cursor and, when
required, retains the prior canonical query payload for execution. Continuation
cursors must advance on every successful page. When a later run receives the
structured transcript, the runtime reconstructs still-live actions from the
transcript's tool-call IDs and its canonical session run log. The action keeps
the same model-facing name across turns without persisting or exposing a second
cursor copy. `storage.Store.ListSessionRunRecords` supplies this canonical history.
Names matching `continue_` plus exactly 24
lowercase hexadecimal characters are reserved for these runtime-generated
tools; agent and toolset registration reject them. Similar authored names such
as `continue_search` and qualified names such as `tools.continue_search` remain
valid. If another call in the same parallel batch requires `finish` recovery,
the runtime closes new domain work but keeps these already-started continuation
actions available. Without a live continuation, the same failure enters
finalization immediately.

Use `Cursor` directly only when repeating the original arguments is part of the
public contract. Truncated results must carry a continuation: bound method
results must define `refinement_hint` (snake_case, optional String) unless
paging is configured, and the runtime rejects truncated results that provide
neither a next cursor nor a refinement hint. `ServerData` attaches rich data
that is never sent to model providers. The registry executor validates each
item with its generated kind-specific codec and persists only canonical JSON;
unknown, duplicate, audience-mismatched, or invalid items fail the result.

### Generated Evaluation Suites

An evaluation is a repeatable test that runs the real product — usually an
agent — and checks that the outcome is still correct. The design declares each
test case and the shape of its input; the application supplies real values and
the code that calls the product:

```go
var QueryEvalInput = Type("QueryEvalInput", func() {
	Attribute("query", String, "Assistant request.", func() { MinLength(1) })
	Required("query")
})

Agent("assistant", "Answers user questions.", func() {
	Suite("assistant", func() {
		Description("Exercises complete assistant outcomes.")
		Timeout("2m")
		Scenario("record_inventory", func() {
			Description("Retrieves the complete record inventory.")
			Input(QueryEvalInput)
			Tags("integration")
		})
	})
})
```

`goa gen` turns each scenario into a typed Go interface method, so a scenario added to the design breaks the build until the application implements it, and input values are validated against the design rules before anything runs. Suites declared inside an `Agent` can also look up the generated contract of every tool that agent can reach, to decode and check recorded tool calls exactly. `goa example` creates a runnable `cmd/<suite>-evals` command once; the application fills in real input values, calls the product, and returns exact pass/fail checks plus plain-English claims about the model's answer. A shared runner selects scenarios by name or tag, limits how many run at once, grades claims with a model-backed judge that must first prove it can tell correct from incorrect answers, and writes a JSON report in design order. See [`docs/evals.md`](docs/evals.md).

### Bookkeeping and Terminal Tools

Use `Bookkeeping()` for control-plane records such as status markers, transition
declarations, or terminal commits. Do not use it for a snapshot whose success
must schedule the next planner turn.

```go
Tool("set_step_status", "Update task step status", func() {
	Args(SetStepStatusRequest)
	Return(TaskProgressSnapshot)
	Bookkeeping()
})

Tool("commit_report", "Commit final report", func() {
	Args(CommitReportRequest)
	Return(CommitReportResult)
	Bookkeeping()
	TerminalRun()
})
```

Bookkeeping tools do not consume the normal `MaxToolCalls` budget, and their
successful results do not reset the recovery-turn counter. Their events are
still durable and streamed, and their provider transcript blocks remain
intact. Successful results stay out of compact future `ToolOutputs`. Every
failure resumes through its typed recovery transition: `correct_call` and
`replan` may use tools, while `finish` resumes without tools so the planner can
synthesize the terminal outcome.

On a `correct_call` recovery turn, the runtime advertises the normal
caller-authorized catalog and attaches correction guidance for every selected
failed call. Historical tool calls remain in the provider transcript for
replay but never restore executable definitions. A `replan` failure removes its
failed tool for that turn unless another selected failure for the same tool is
correctable. If the planner still requests an excluded tool, the runtime rejects
the planner output before any sibling call executes. Planner-owned await barriers remain strict because they encode
suspension rather than a direct model tool request. Caller `WithRestrictToTool`
policy remains run-scoped.

The workflow runtime evaluates one admitted planner result as one step: it executes tool and await work, records durable and planner-facing outputs through one canonical path, then applies one transition policy to resume, finish, or finalize. A terminal payload may only accompany successful, non-terminal bookkeeping side effects; budgeted tools, failed bookkeeping tools, terminal tools, and awaits must be separate planner decisions. Bookkeeping calls remain in the provider transcript so signed responses are never edited.

A planner that knows a successful selected tool batch will provide the final
evidence can set `PlanResult.SynthesizeAfterTools`. The durable workflow carries
that decision to the next activity as `PlanResumeInput.SynthesisOnly`; the
runtime requires the planner to return a terminal result without additional
tool calls. A failed tool follows its structured `ToolFailure.Recovery`
directive first. `correct_call` supplies structured correction evidence while
leaving the planner free to retry, combine work, select another advertised
capability, await input, or answer. `replan` removes the failed tool from the
recovery turn while permitting another advertised action, input request, or
answer. `finish` enters finalization and forbids further domain work. The
planner may return a final response or registered terminal bookkeeping calls.
The runtime enforces the advertised catalog, generated payload contracts, and execution
caps; it does not infer how many semantic operations the planner must repeat.
When one tool has both correction and replan failures in the same batch, the
correctable failure keeps that tool available.

`MaxRecoveryTurns` counts replacement planner activities scheduled after
rejected tool output, a rejected model invocation, or a rejected completed
answer. Bookkeeping calls do not consume or reset this budget. If a rejected
bookkeeping result schedules another planner activity, that replacement
activity consumes one recovery turn. Finalization uses the same budget: a
rejected finalizer response or a terminal tool failure marked `correct_call`
retains the finalization restriction while the model replaces that output.
Other terminal-tool failures still end finalization.

Agent-as-tool results use this same typed transition contract. The number of
child tools observed during the nested run is telemetry for linked progress;
zero children does not turn a success or correctable failure into run
finalization.

Recovery turns carry the selected failed call IDs in `PlanActivityInput`.
Empty IDs are omitted from start and ordinary resume activities. Runtime
workers, generated packages, and callers must use the same generated input
contract; mixed shapes are unsupported.

A model invocation rejected before a canonical response exists carries a
separate `ModelInvocationRecovery` value instead of failed call IDs. It carries
exactly one bounded fact: fixed malformed-JSON guidance, advertised-input
correction text, or the untouched provider-returned name of a tool absent from
that request's catalog. Malformed argument bytes stay private. The rejected
response stays out of history, and the normal caller-authorized executable
catalog remains available for the replacement.
Existing Temporal histories replay unchanged. Histories that contain this new
activity result require workers running the matching runtime; mixed older and
newer workers, and rollback to an older worker, are unsupported for those
histories. See [`docs/runtime.md`](docs/runtime.md) for the full recovery
contract.

The flag is valid only on a tool-only result, keeping execution and answer
synthesis as separate turns without relying on process-local state. The batch
must contain at least one budgeted tool and cannot contain a terminal tool;
bookkeeping and terminal-run semantics therefore remain independent. See
[DESIGN.md](DESIGN.md#planner-step-contract) for the complete transition table.

---

## Runtime and Observability

Every run follows the same lifecycle:

```text
Start -> PlanStart -> execute admitted tools -> PlanResume -> ... -> final response
                     \-> await clarification / confirmation / external results
                     \-> child workflow for agent-as-tool
                     \-> terminal tool result
```

The runtime emits typed hook and stream events for:

- run start, phase changes, completion, cancellation, and failure
- prompt rendering and prompt provenance
- tool scheduled, updated, completed, failed, and authorized
- assistant chunks, final messages, planner thoughts, thinking blocks, and token usage
- awaits for clarification, external tools, and confirmation
- child run links for agent-as-tool composition

Prompt rendering itself does not write runtime storage. A
`prompt.RenderRecorder` records the resolved prompt ID, version, and scope for
each successful render when its context is passed to `PromptRegistry.Render`.
Callers that render text before `Start` pass `recorder.Events()` with
`runtime.WithRenderedPrompts(...)`; the accepted workflow stores those events
after its start record and before planning. Planner activities, consumer-side
child prompt rendering, and `RunOneShot` produce the same `prompt.RenderEvent`
and durable `PromptRendered` record. They differ only in how the event reaches
the accepted run. `recorder.Events()` sorts completed renders by prompt
identity, version, session, and scope, so concurrent render completion cannot
change the exact workflow start request. Consumer-side child rendering runs in an activity, so a
replayed workflow reuses the prompt text and render events already recorded in
workflow history instead of reading prompt storage again.

Sessionful callers supply a stable run ID before asking the engine to start.
The engine binds that ID to the exact start request while the execution remains
queryable in the backend. During that period, an exact retry returns the
original open or closed execution and a changed request returns a typed
conflict. The shared versioned recipe digest includes the caller-submitted workflow,
task queue, input payload, timeout, retry policy, memo, and search attributes
without collapsing native payload types. After backend history expires, the
owning application must use its durable command identity and must not reopen a
settled obligation. Every root and child request requires its engine workflow ID
to equal `RunInput.RunID`. A zero `engine.RetryPolicy` leaves retry behavior at
the engine default. When a caller supplies a policy, `MaxAttempts` includes the
first attempt; retry timing is accepted only with a positive `MaxAttempts` or
`UnlimitedAttempts`.

Custom workflow engines call `engine/contract.NormalizeRootRequest` before
retaining a root request. The returned request owns all mutable values, and its
digest is the value the engine binds to the workflow ID for exact-retry checks.
Child starts use `contract.NormalizeChildRequest`. Before every initial or retry
attempt, the engine gives the workflow handler a fresh input from
`contract.CopyRunInput`. It retains one private `contract.CopyRunOutput` result
and makes another copy for every wait, query, or other caller-facing read.
Shared normalization fixes the portable search values and digest; each adapter
still owns submission to its backend. These functions apply the same validation,
copying, and size rules as the shipped engines without exposing backend types.

The accepted workflow owns its lifecycle records. Its first
activity calls `StartRootRun`, `StartChildRun`, `StartOneShotRun`, or
`StartOneShotChildRun`. Sessionful starts serialize with the active-to-ended
transition on the owning Session record. An active Session produces running
metadata. An ended Session produces terminal canceled metadata with reason
`session_ended` and no planner or tool work.

The `storage.Store` interface starts sessionful roots and children plus
sessionless roots and children. It also records cancellation, suspension, and
terminal outcomes. Prompt references and child relationships are derived from
ordered run records instead of being copied into run metadata. A root start
always stores `RunStarted` and adds a canceled
`RunCompleted` record when the Session has ended. A child start always stores
the parent link followed by `RunStarted` and adds the canceled record when the
Session has ended. A sessionless child start atomically stores its parent link
and `RunStarted`. The returned `StartOutcome` tells the workflow whether it may
proceed. Cancellation,
suspension, and terminal methods store the lifecycle change and its matching
record together, so there is no partial state for another runtime replica to
repair. The hook bus receives saved records afterward and does not write
lifecycle state. The first start retries temporary storage failures without an
attempt ceiling, and planner or tool work cannot begin until it is durable.
Malformed records and stable-key conflicts fail immediately as non-retryable
contract errors.

The in-memory engine sends every successful root or child output through the
same strict, 1 MiB converter used at the Temporal workflow boundary before it
retains or returns the value. The caller receives an independent copy, so local
tests cannot pass with an oversized or unserializable output that Temporal would
reject. Temporal child starts wait for Temporal to accept or reject the child ID
before returning a handle; waiting on that handle is only for child completion.

Cancellation stores the first reason and its matching record before engine
cancellation and never rolls it back after an engine error. Active metadata
paired with a missing engine workflow is an invariant error; if neither
metadata nor a workflow exists, cancellation is idempotently complete. Root,
child, and one-shot runs use the same durable run metadata. A later different
cancellation reason conflicts.

Normal workflows retry their suspension and terminal writes until the runtime
store accepts them. If a workflow closes in the engine while its stored run is
still active, an operator may call `Runtime.EnsureRunCompletion` with that run
ID. The command reads the final engine result and stores the missing suspension
or terminal record through a repair-only store method. If the run is already
closed, it validates and redelivers the exact stored result. If the workflow
stores another final record while the command is running, that stored record
remains authoritative and is the one redelivered. For a child run, the command
also validates its stored start and redelivers the exact parent link before the
terminal stream events. A recovered record uses the completion time returned by
the engine, so every retry submits the same record timestamp.
Both ensure commands require `Runtime.WithStream` while their Session is active.
When the store response reports that this process inserted the completion, the
runtime notifies the local hook bus once before stream delivery. A later retry
only redelivers the exact stored stream events. An ended Session keeps its
durable result and suppresses stream delivery. The Session status returned with
the stored record decides this: an event accepted while active remains due even
if the Session ends while delivery is retrying.
Hosts that must restore several nested children before replaying their final
results can call `Runtime.EnsureChildRunLink` in parent-first order. The command
validates and redelivers only the exact stored parent link; it does not deliver
the child's final result. A new child start requires its parent to be running,
while an exact retry of an already stored child start remains valid after the
parent stops.
All accepted lifecycle timestamps use millisecond precision because runtime
records carry time as integer milliseconds.
`GetRunSnapshot`, `ListRunEvents`, and other reads never change stored state.

`RunOneShot` stores the run before invoking its callback. After the callback
returns, it records every prompt render and the terminal result with a context
that is independent of callback cancellation. Temporary store failures retry
without invoking the callback again.

Before deploying recipe validation over workflows started by an older runtime,
pause new admissions and prove that no unresolved start obligation or active
workflow still needs duplicate attachment. Then deploy every writer together.
A queryable execution without the reserved recipe memo is a conflict; the
runtime never infers its original request.

Wire a stream sink for real-time UIs:

```go
rt := runtime.New(
	runtimeStore,
	runtime.WithStream(mySink),
	runtime.WithMemoryStore(memoryStore),
	runtime.WithLogger(logger),
	runtime.WithMetrics(metrics),
	runtime.WithTracer(tracer),
)
```

For model streaming inside planners, choose one style per planner call:

- `PlannerContext.PlannerModelClient(id)` is recommended for the selected, single model call. It records assistant and thinking output with that invocation and returns a `planner.StreamSummary`; the runtime publishes presentation after the planner selects the response.
- `PlannerContext.ModelClient(id)` gives you direct access to a `model.Client`.
  Its `Stream` method returns a validated stream; pair that value with
  `planner.ConsumeStream` or drain it yourself when you need lower-level
  control.

The runtime captures each model response before planner code sees it. When a
planner probes through the opaque client, goa-ai matches returned model-facing tool
calls to the exact response that produced them, publishes only that response's
presentation, and replays only that transcript. Usage events still include all
attempts. Every stream exposes closed typed chunks, then makes its canonical
response available separately after clean EOF. Model gateways carry that
response independently from planner-facing chunks, and terminal helpers return
the selected provider message without exposing transcript identity. Future
session turns retain provider-authored thinking without inferring ownership from
visible text.
Planners return a tool name and canonical payload. A request forwarded from a
model call also carries the provider correlation ID; planner-authored requests
do not. The runtime assigns every accepted request its deterministic execution
ID. When planner code compiles a model-facing action into different executable
intent, the runtime stores the original model name and payload separately;
planners do not populate `ModelName` or `ModelPayload`. The workflow commits the
selected response once after atomic admission and before effects. Usage includes
all attempts. Provider tool-call IDs remain opaque and unchanged in durable
transcripts.
Provider adapters translate IDs only while encoding a request when the target
wire protocol imposes narrower syntax, and apply the same request-local alias
to each matching tool result.

---

## MCP and Registries

### Consume MCP Servers

Use `FromMCP` for MCP servers declared in the same Goa design. Use `FromExternalMCP` when the server is external and the Goa design owns the local schema contract.

```go
var LocalAssistantTools = Toolset(FromMCP("assistant", "assistant-mcp"))

var RemoteSearch = Toolset("remote-search", FromExternalMCP("remote", "search"), func() {
	Tool("web_search", "Search the web", func() {
		Args(func() {
			Attribute("query", String, "Search query")
			Required("query")
		})
		Return(func() {
			Attribute("results", ArrayOf(String), "Search results")
			Required("results")
		})
	})
})

Agent("chat", "MCP-enabled assistant", func() {
	Use(LocalAssistantTools)
	Use(RemoteSearch)
})
```

Runtime MCP callers support stdio and HTTP through `runtime/mcp`. The HTTP
caller accepts tool results returned as JSON or as an HTTP event stream.

### Expose Goa Services as MCP Servers

```go
Service("calculator", func() {
	MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))
	JSONRPC(func() {
		POST("/mcp")
	})

	Method("add", func() {
		Payload(func() {
			Attribute("a", Int, "First number")
			Attribute("b", Int, "Second number")
			Required("a", "b")
		})
		Result(func() {
			Attribute("sum", Int, "Sum")
			Required("sum")
		})
		Tool("add", "Add two numbers")
	})
})
```

The generated MCP adapter maps Goa methods to JSON-RPC tools, resources, and
static prompts.

This preview changes the generated MCP surface. The
[preview upgrade guide](docs/runtime.md#preview-upgrade-guide) lists every
removed API and the supported replacement.
The same guide requires a coordinated runtime cutover: finish or cancel old
active workflows and abandon unresolved old start attempts before deploying the
new workers. The new runtime does not replay or retry those old requests.

### Discover Tools Through Registries

For independently deployed tool providers, declare a registry source and use registry-backed toolsets.

```go
var CorpRegistry = Registry("corp", func() {
	URL("https://registry.corp.internal")
	Security(CorpAPIKey)
	SyncInterval("5m")
	CacheTTL("1h")
})

var DataTools = Toolset(FromRegistry(CorpRegistry, "data-tools"), func() {
	Version("1.2.3")
})

Agent("analyst", "Data analysis agent", func() {
	Use(DataTools)
})
```

There are three registry layers:

- `Registry(...)` and `FromRegistry(...)` in the DSL declare dynamic catalog sources.
- `gen/<service>/registry/<name>/` contains generated agent-side registry clients and helpers.
- `runtime/toolregistry` and `registry/` provide the Pulse wire protocol and clustered gateway for health-monitored cross-process invocation.

Generated `registry.go` files in agent packages are local runtime registration helpers; they are not the clustered registry service.

---

## Production

Start simple with `runtime.New(storageinmem.New())`. Move to production by adding durable execution, a host-owned runtime store, model providers, stream delivery, policy, and telemetry.
When a tracer is configured, goa-ai emits OpenTelemetry GenAI semantic-convention
spans for planner-scoped model calls (`chat {model}`), tool calls
(`execute_tool {tool}`), and agent-as-tool delegation (`invoke_agent {agent}`).
These spans carry conversation ID, agent identity, model request/response
fields, token usage, finish reasons, and streaming time-to-first-chunk where
available. Prompt text, chat history, tool arguments, and tool results are not
recorded by default.
Planner call spans also carry the exact advertised tool count and names. The
clustered registry emits elected per-toolset readiness spans. See the
[runtime trace contract](docs/runtime.md#registry-and-model-request-traces) for
the attribute meanings and failure behavior.

```go
eng, err := temporal.NewWorker(temporal.Options{
	ClientOptions: &client.Options{
		HostPort:  "temporal:7233",
		Namespace: "default",
	},
	WorkerOptions: temporal.WorkerOptions{
		TaskQueue: "orchestrator_chat_workflow",
	},
})
if err != nil {
	log.Fatal(err)
}
defer eng.Close()

rt := runtime.New(
	runtimeStore,
	runtime.WithEngine(eng),
	runtime.WithMemoryStore(memoryStore),
	runtime.WithPromptStore(promptStore),
	runtime.WithStream(streamSink),
	runtime.WithPolicy(policyEngine),
	runtime.WithLogger(logger),
	runtime.WithMetrics(metrics),
	runtime.WithTracer(tracer),
)

modelClient, err := rt.NewOpenAIModelClient(runtime.OpenAIConfig{
	APIKey:       os.Getenv("OPENAI_API_KEY"),
	DefaultModel: "gpt-5-mini",
	HighModel:    "gpt-5",
	SmallModel:   "gpt-5-nano",
	MaxTokens:    4096,
})
if err != nil {
	log.Fatal(err)
}
if err := rt.RegisterModel("default", modelClient); err != nil {
	log.Fatal(err)
}

// Vertex AI (ADC auth): Gemini for the small tier, Claude for default/high.
gemini, err := rt.NewVertexGeminiModelClient(ctx, runtime.VertexConfig{
	ProjectID:    project,
	Location:     "global",
	DefaultModel: "gemini-2.5-flash",
})
// ...
claude, err := rt.NewVertexAnthropicModelClient(ctx, runtime.VertexConfig{
	ProjectID:    project,
	Location:     "global",
	DefaultModel: "claude-sonnet-5",
	HighModel:    "claude-opus-4-8",
})

if err := chat.RegisterUsedToolsets(ctx, rt, chat.WithHelpersExecutor(helperExec)); err != nil {
	log.Fatal(err)
}
if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{Planner: chatPlanner}); err != nil {
	log.Fatal(err)
}

sealCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
defer cancel()
if err := rt.Seal(sealCtx); err != nil {
	log.Fatal(err)
}
```

Production checklist:

- Keep all model-facing schemas in the DSL. Regenerate instead of hand-editing `gen/`.
- Preserve generated tool input projections across model gateways and proxies:
  schema, schema without the root example, and parsed example input should move
  as one provider-neutral `model.ToolInputContract` until the provider adapter
  chooses the final projection.
- Gemini function declarations translate `oneOf` choices to `anyOf` and omit
  only validation and annotation keywords that Vertex does not accept. The
  validated client still applies the complete original schema to every returned
  tool call; unknown structural keywords fail before a request is sent.
- Keep model gateways raw: compose provider-side behavior around
  `model.Provider`, and construct the validated `model.Client` after the remote
  transport so the request owner's advertised schema and attached decoder
  remain authoritative.
- Register models, toolsets, agents, stores, streams, policy, and telemetry before the first run.
- Call `rt.Seal(ctx)` for worker processes before serving traffic; Temporal workers start at the seal boundary.
- Supply Temporal connection settings through `ClientOptions`; the engine always
  installs the strict Goa-AI data converter and limits one workflow or activity
  call to 1 MiB. Persist larger tool results first and return their durable
  reference.
- Create the session through the host application and use `WithRunID` before sessionful `Run`/`Start`, or use
  `OneShotRun`/`StartOneShot` for sessionless work where the runtime may create
  the run ID.
- Use persistent stores for transcripts, prompt overrides, and runtime storage
  when runs must survive process restarts. The application's session owner
  creates, ends, and purges sessions. Give runtime workers a persistent
  `storage.Store` for run metadata, continuation checkpoints, ordered records,
  and exact rejected-model evidence.
- Implement the complete `storage.Store` contract in one host-owned durable
  repository. Deploy its schema and every caller together; mixed storage
  contracts are unsupported.
- Use stream events rather than polling for UI updates.
- Put irreversible or operator-sensitive actions behind `Confirmation(...)`.
- Use `BoundedResult()` and `ServerData(...)` for large data so models see bounded summaries while UIs retain full-fidelity data.

---

## Generated Layout

| Path | What it contains |
| --- | --- |
| `gen/<service>/agents/<agent>/` | Agent ID, route, typed client, workflow/activity names, registration helpers |
| `gen/<service>/agents/<agent>/specs/` | Aggregated agent tool catalog and `tool_schemas.json` |
| `gen/<service>/toolsets/<toolset>/` | Tool payload/result/server-data types, codecs, specs, transforms, provider adapters |
| `gen/<service>/completions/` | Service-owned typed results, private structured-output specs/codecs, public unary/streaming wrappers, and immutable example accessors |
| `gen/<service>/registry/<name>/` | Generated registry client and discovery helpers |
| `gen/mcp_<service>/` | Generated MCP adapter code for services that declare `MCP(...)` |
| `internal/agents/` | Application-owned scaffold from `goa example`: bootstrap, planner stubs, tool adapters |
| `AGENTS_QUICKSTART.md` | Contextual generated wiring guide for the module |

---

## Feature Packages

| Package | Purpose |
| --- | --- |
| `runtime/agent/runtime` | Runtime, clients, run options, policy overrides, stores, registration |
| `runtime/agent/planner` | Planner interfaces, plan results, tool requests, streaming helpers |
| `runtime/agent/model` | Provider-neutral model client, messages, tool definitions, streaming chunks |
| `runtime/agent/engine/inmem` | In-memory development engine |
| `runtime/agent/engine/temporal` | Temporal worker/client engine |
| `runtime/agent/storage/inmem` | Integrated in-memory runtime store for local development and tests |
| `runtime/mcp` | MCP callers for stdio and HTTP |
| `runtime/toolregistry` | Registry wire protocol, executor, provider support, schema validation |
| `features/model/openai` | OpenAI Responses API adapter |
| `features/model/bedrock` | Amazon Bedrock adapters for Converse and native Claude Messages over InvokeModel, with exact Runtime/Mantle token counting |
| `features/model/anthropic` | Anthropic Messages adapter with streaming and exact token counting; also composes with compatible gateways such as Bedrock Mantle |
| `features/model/vertex` | Google Vertex AI adapters: Gemini (`vertex.New`) and Claude-on-Vertex (`vertex.NewAnthropicClient`), both with native token counting and provider-error classification. |
| `features/model/gateway` | Remote model gateway client |
| `features/model/middleware` | Rate limiting, logging, metrics middleware |
| `features/memory/mongo` | Mongo-backed transcript memory store |
| `features/prompt/mongo` | Mongo-backed prompt override store |
| `features/stream/pulse` | Pulse/Redis stream sink and subscribers |
| `features/policy/basic` | Basic policy engine for tool filtering and caps |
| `registry` | Clustered registry service for cross-process tool discovery and invocation |

---

## Common Questions

### What should go in the DSL versus application code?

Put stable contracts in the DSL: agent names, tool schemas, validations, completion schemas, policy defaults, tags, confirmation requirements, bounded-result contracts, MCP exposure, and registry sources. Put runtime choices in application code: planner implementation, model provider, stores, streams, telemetry, deployment, per-run overrides, and service logic.

### Do I have to use Temporal?

No. `runtime.New(storageinmem.New())` uses the in-memory engine and integrated
in-memory store and is ideal for local development and tests. Use the Temporal
engine and a host-owned durable store when runs must survive worker restarts,
support asynchronous coordination, or scale across worker processes.

### How do agents use tools?

Planners receive `AdvertisedToolDefinitions()` and return `planner.ToolRequest` values. The runtime validates payloads with generated codecs, executes the matching toolset, records the result, and calls `PlanResume` with canonical tool outputs.

### How do I make a long-running UI?

Configure a stream sink or Pulse runtime streams. Subscribe by session/run, render typed events, and treat `run_stream_end` or terminal `workflow` events as completion markers. Child agents are linked with `child_run_linked` events instead of flattening nested streams.

### How do I avoid huge tool results in prompts?

Declare `BoundedResult()` and make the service return a bounded semantic result plus `planner.ToolResult.Bounds`. Attach full-fidelity data with `ServerData(...)` when observers need charts, tables, maps, evidence, or downstream attachments.

### How do I expose existing services to external agents?

Use `MCP(...)` on a Goa service, mark methods with `Tool(...)` or
`Resource(...)`, and declare service-level prompts with `StaticPrompt(...)`.
Declare the HTTP endpoint with a service-level JSON-RPC `POST` route, such as
`JSONRPC(func() { POST("/mcp") })`. Goa-AI generates MCP adapter code while Goa
still owns service and transport generation.

---

## Best Practices

- Design first: contracts belong in `design/*.go`; generated code is the artifact, not the source of truth.
- Add descriptions, examples, and validations. Better schemas make better tool calls and correction directives.
- Use generated codecs and clients. Do not hand-encode tool payloads or structured completion results.
- Keep planners focused on decisions. Service methods and tool executors perform side effects.
- Use `PlannerModelClient` for streaming unless you need raw stream control.
- Use tags and policy clauses to narrow tool availability before model prompting and again before execution.
- Prefer agent-as-tool for specialist delegation when you want isolated runs, linked observability, and durable child workflows.
- Use confirmations for sensitive tools and bounded/server-data contracts for large or UI-rich results.
- Regenerate after every DSL change: `goa gen`, then `goa example` when you want scaffold updates.

---

## Requirements

- Go 1.25.5+ for this repository
- Goa v3 CLI: `go install goa.design/goa/v3/cmd/goa@v3.31.0-preview.3`
- Optional for production: Temporal Server 1.31+, MongoDB, Redis/Pulse

Temporal Server 1.31 or newer is required for planner time budgets because it
identifies server-owned Schedule-to-Close expiration as a timeout. Older
servers report that boundary as a non-retryable activity failure, which cannot
be distinguished safely from planner code returning a timeout-shaped error.

---

## Learn More

| Resource | Use it for |
| --- | --- |
| [`quickstart/README.md`](quickstart/README.md) | Copy-paste runnable project setup |
| [`docs/overview.md`](docs/overview.md) | Architecture and mental model |
| [`docs/dsl.md`](docs/dsl.md) | Complete DSL reference and patterns |
| [`docs/runtime.md`](docs/runtime.md) | Runtime API, planners, engines, stores, streaming, policies |
| [`docs/evals.md`](docs/evals.md) | Generated evaluation DSL, hooks, judging, and reports |
| [`DESIGN.md`](DESIGN.md) | Generator design and repository architecture |
| [Goa-AI docs](https://goa.design/docs/2-goa-ai/) | Published guides |
| [Go package docs](https://pkg.go.dev/goa.design/goa-ai) | API reference |

---

## Contributing

Issues and PRs are welcome. Include a Goa design, a failing test, or a clear reproduction when reporting behavior. See [`AGENTS.md`](AGENTS.md) for repository guidelines.

Run `make setup` once after cloning or when `.tool-versions` or `.go-install`
changes. It installs the exact protobuf compiler and Go generators used by CI.
Normal `make` targets verify those versions before building or generating code.

## License

MIT License (C) Raphael Simon and the [Goa community](https://goa.design).

<p align="center">
  <i>Build agent systems with contracts you can read, code you can trust, and runtime behavior you can operate.</i>
</p>
