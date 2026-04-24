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
| Strong tool contracts | Goa types, validations, examples, generated JSON Schema, generated codecs |
| Durable agent execution | A plan/execute/resume workflow loop with retries, budgets, cancellation, and Temporal support |
| Existing service logic | `BindTo` and generated transforms that connect tools to Goa service methods |
| Structured final answers | Service-owned `Completion(...)` contracts with unary and streaming helpers |
| Multi-agent systems | First-class agent-as-tool composition with child runs and linked streams |
| Human approval | Await/clarification flows plus design-time and runtime tool confirmation |
| Real-time UI | Typed stream events for tool progress, assistant text, usage, awaits, workflow status, and child links |
| External tools | MCP callers, generated MCP servers, external MCP schemas, and registry-backed discovery |
| Production operations | Mongo-backed stores, Pulse streaming, OpenAI/Bedrock/Anthropic/gateway clients, telemetry hooks |

Goa-AI is not a prompt wrapper. It is a contract and runtime layer for agentic Go services.

---

## Quick Start

This path gives you a generated, runnable agent and a typed direct-completion helper. The generated example uses the in-memory engine, so there are no external services required.

### 1. Create a Module

```bash
go install goa.design/goa/v3/cmd/goa@latest

mkdir quickstart && cd quickstart
go mod init example.com/quickstart
go get goa.design/goa/v3@latest goa.design/goa-ai@latest
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
			DefaultCaps(MaxToolCalls(2), MaxConsecutiveFailedToolCalls(1))
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
Assistant: Hello from example planner.
Completion draft_task: ...
Completion stream draft_task: ...
```

Generation creates application-owned scaffolding under `internal/agents/` and generated contract code under `gen/`. Edit the planner and bootstrap files; do not edit `gen/`.

### 4. Run an Agent from Application Code

The generated agent package exposes a typed client. Sessionful runs require an explicit session; one-shot runs do not.

```go
rt, cleanup, err := bootstrap.New(ctx)
if err != nil {
	log.Fatal(err)
}
defer cleanup()

if _, err := rt.CreateSession(ctx, "session-1"); err != nil {
	log.Fatal(err)
}

client := chat.NewClient(rt)
out, err := client.Run(ctx, "session-1", []*model.Message{{
	Role:  model.ConversationRoleUser,
	Parts: []model.Part{model.TextPart{Text: "Hello"}},
}})
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

Planners decide what happens next: final response, tool calls, await human input, or terminal tool result. Tool executors decide how work is performed.

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
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: summary.Text}},
			},
		},
		Streamed: true,
	}, nil
}
```

Register model clients during bootstrap with `rt.RegisterModel(...)` or runtime factories such as `rt.NewOpenAIModelClient(...)` and `rt.NewBedrockModelClient(...)`.

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

Generated specs include model-facing schemas and runtime-facing codecs. Invalid payloads fail at the boundary and can produce structured retry hints rather than string parsing.

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

The generator emits typed transforms where shapes are compatible. Runtime metadata supplies supported injected fields such as `run_id`, `session_id`, `turn_id`, and `tool_call_id`.

### Structured Direct Completions

Use `Completion(...)` when the model should return a typed value directly instead of calling a tool.

```go
var Draft = Type("Draft", func() {
	Attribute("name", String, "Task name")
	Attribute("goal", String, "Outcome-style goal")
	Required("name", "goal")
})

var _ = Service("tasks", func() {
	Completion("draft_from_transcript", "Produce a task draft directly", func() {
		Return(Draft)
	})
})
```

`goa gen` emits `gen/<service>/completions/` with schemas, codecs, `completion.Spec` values, `Complete<Name>(...)`, `StreamComplete<Name>(...)`, and `Decode<Name>Chunk(...)`. Completion names are part of the contract: 1-64 ASCII characters, letters/digits/`_`/`-`, starting with a letter or digit.

Unary helpers request provider-enforced structured output and decode with generated codecs. Streaming helpers expose preview `completion_delta` chunks but decode only the final canonical `completion` chunk. Providers that cannot preserve the structured-output contract fail explicitly with `model.ErrStructuredOutputUnsupported`.

### Agent-as-Tool Composition

Agents can export toolsets that other agents use. The nested agent runs as a child workflow, not as flattened helper code.

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

Parent runs receive a tool result with a child run link. Streams emit `child_run_linked` so UIs can render nested runs without losing identity, logs, or telemetry.

### Runtime Policies, Tags, and Timing

Policies are runtime-enforced, not planner suggestions.

```go
Agent("operator", "Production operations agent", func() {
	RunPolicy(func() {
		DefaultCaps(MaxToolCalls(20), MaxConsecutiveFailedToolCalls(3))
		Timing(func() {
			Budget("5m")
			Plan("45s")
			Tools("90s")
		})
		InterruptsAllowed(true)
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

Per-run options can further restrict execution:

```go
out, err := client.Run(ctx, "session-1", messages,
	runtime.WithRunTimeBudget(2*time.Minute),
	runtime.WithRestrictToTool("docs.search"),
	runtime.WithTagPolicyClauses([]runtime.TagPolicyClause{
		{AllowedAny: []string{"read", "safe"}},
		{DeniedAny: []string{"destructive"}},
	}),
)
```

### Human-in-the-Loop and Confirmation

Planners can pause for clarification or external tool results. Sensitive tools can require approval before execution.

```go
Tool("change_setpoint", "Change a device setpoint", func() {
	Args(ChangeSetpointRequest)
	Return(ChangeSetpointResult)
	Confirmation(func() {
		Title("Confirm setpoint change")
		PromptTemplate("Set {{ .DeviceID }} to {{ .Value }}?")
		DeniedResultTemplate(`{"status":"denied"}`)
	})
})
```

At runtime, the workflow emits an await-confirmation event, waits for `ProvideConfirmation`, records a durable authorization event, and only then executes the tool. Denials produce schema-compliant tool results so planners and transcripts remain deterministic.

### Bounded Results and Server Data

Large results need two views: a small model-facing view and rich server-side data for UIs or downstream systems.

```go
Tool("get_time_series", "Get a bounded time-series view", func() {
	Args(TimeSeriesRequest)
	Return(TimeSeriesSummary)
	BoundedResult(func() {
		Cursor("cursor")
		NextCursor("next_cursor")
	})
	ServerData("charts.points", TimeSeriesPoints, func() {
		Description("Chart points for observer-facing UI")
		AudienceInternal()
		FromMethodResultField("ChartPoints")
	})
	ServerDataDefault("off")
})
```

`BoundedResult` makes truncation explicit through runtime-owned bounds metadata (`returned`, `truncated`, optional `total`, `next_cursor`, and `refinement_hint`). `ServerData` attaches rich data that is never sent to model providers.

### Bookkeeping and Terminal Tools

Use `Bookkeeping()` for control-plane side effects such as status updates, progress snapshots, or terminal commits.

```go
Tool("set_step_status", "Update task step status", func() {
	Args(SetStepStatusRequest)
	Return(TaskProgressSnapshot)
	Bookkeeping()
	PlannerVisible()
})

Tool("commit_report", "Commit final report", func() {
	Args(CommitReportRequest)
	Return(CommitReportResult)
	Bookkeeping()
	TerminalRun()
})
```

Bookkeeping tools do not consume the normal `MaxToolCalls` budget. Their events are still durable and streamed. Results stay hidden from future planner turns unless `PlannerVisible()` opts them back in.

The workflow runtime evaluates one admitted planner result as one step: it executes tool and await work, records durable and planner-visible outputs through one canonical path, then applies one transition policy to resume, finish, or finalize. A terminal payload may only accompany hidden, non-terminal bookkeeping side effects; budgeted tools, planner-visible bookkeeping, terminal tools, and awaits must be separate planner decisions.

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

Wire a stream sink for real-time UIs:

```go
rt := runtime.New(
	runtime.WithStream(mySink),
	runtime.WithMemoryStore(memoryStore),
	runtime.WithRunEventStore(runLogStore),
	runtime.WithLogger(logger),
	runtime.WithMetrics(metrics),
	runtime.WithTracer(tracer),
)
```

For model streaming inside planners, choose one style per planner call:

- `PlannerContext.PlannerModelClient(id)` is recommended. It owns assistant/thinking/usage event emission and returns a `planner.StreamSummary`.
- `PlannerContext.ModelClient(id)` gives you a raw `model.Client`. Pair it with `planner.ConsumeStream` or drain the stream yourself when you need lower-level control.

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

Runtime MCP callers support stdio, HTTP, and SSE transports through `runtime/mcp`.

### Expose Goa Services as MCP Servers

```go
Service("calculator", func() {
	MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))

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

The generated MCP adapter maps Goa methods to JSON-RPC tools, resources, prompts, notifications, subscriptions, and SSE where appropriate.

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

Start simple with `runtime.New()`. Move to production by adding durable execution, persistent stores, model providers, stream delivery, policy, and telemetry.

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
	runtime.WithEngine(eng),
	runtime.WithMemoryStore(memoryStore),
	runtime.WithSessionStore(sessionStore),
	runtime.WithRunEventStore(runLogStore),
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
- Register models, toolsets, agents, stores, streams, policy, and telemetry before the first run.
- Call `rt.Seal(ctx)` for worker processes before serving traffic; Temporal workers start at the seal boundary.
- Use `CreateSession` before sessionful `Run`/`Start`, or use `OneShotRun`/`StartOneShot` for sessionless work.
- Use persistent stores for transcripts, sessions, prompt overrides, and run logs when runs must survive process restarts.
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
| `gen/<service>/completions/` | Service-owned structured-output specs, codecs, unary and streaming helpers |
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
| `runtime/mcp` | MCP callers for stdio, HTTP, and SSE |
| `runtime/toolregistry` | Registry wire protocol, executor, provider support, schema validation |
| `features/model/openai` | OpenAI Responses API adapter |
| `features/model/bedrock` | AWS Bedrock adapter, including visible Claude thinking support |
| `features/model/anthropic` | Direct Anthropic adapter |
| `features/model/gateway` | Remote model gateway client |
| `features/model/middleware` | Rate limiting, logging, metrics middleware |
| `features/memory/mongo` | Mongo-backed transcript memory store |
| `features/session/mongo` | Mongo-backed session store |
| `features/runlog/mongo` | Mongo-backed append-only run event store |
| `features/prompt/mongo` | Mongo-backed prompt override store |
| `features/stream/pulse` | Pulse/Redis stream sink and subscribers |
| `features/policy/basic` | Basic policy engine for tool filtering and caps |
| `registry` | Clustered registry service for cross-process tool discovery and invocation |

---

## Common Questions

### What should go in the DSL versus application code?

Put stable contracts in the DSL: agent names, tool schemas, validations, completion schemas, policy defaults, tags, confirmation requirements, bounded-result contracts, MCP exposure, and registry sources. Put runtime choices in application code: planner implementation, model provider, stores, streams, telemetry, deployment, per-run overrides, and service logic.

### Do I have to use Temporal?

No. `runtime.New()` uses the in-memory engine by default and is ideal for local development and tests. Use the Temporal engine when runs must survive worker restarts, support asynchronous coordination, or scale across worker processes.

### How do agents use tools?

Planners receive `AdvertisedToolDefinitions()` and return `planner.ToolRequest` values. The runtime validates payloads with generated codecs, executes the matching toolset, records the result, and calls `PlanResume` with canonical tool outputs.

### How do I make a long-running UI?

Configure a stream sink or Pulse runtime streams. Subscribe by session/run, render typed events, and treat `run_stream_end` or terminal `workflow` events as completion markers. Child agents are linked with `child_run_linked` events instead of flattening nested streams.

### How do I avoid huge tool results in prompts?

Declare `BoundedResult()` and make the service return a bounded semantic result plus `planner.ToolResult.Bounds`. Attach full-fidelity data with `ServerData(...)` when observers need charts, tables, maps, evidence, or downstream attachments.

### How do I expose existing services to external agents?

Use `MCP(...)` on a Goa service and mark methods with `Tool(...)`, `Resource(...)`, prompts, notifications, or subscriptions. Goa-AI generates MCP adapter code while Goa still owns service and transport generation.

---

## Best Practices

- Design first: contracts belong in `design/*.go`; generated code is the artifact, not the source of truth.
- Add descriptions, examples, and validations. Better schemas make better tool calls and better retry hints.
- Use generated codecs and clients. Do not hand-encode tool payloads or structured completion results.
- Keep planners focused on decisions. Service methods and tool executors perform side effects.
- Use `PlannerModelClient` for streaming unless you need raw stream control.
- Use tags and policy clauses to narrow tool availability before model prompting and again before execution.
- Prefer agent-as-tool for specialist delegation when you want isolated runs, linked observability, and durable child workflows.
- Use confirmations for sensitive tools and bounded/server-data contracts for large or UI-rich results.
- Regenerate after every DSL change: `goa gen`, then `goa example` when you want scaffold updates.

---

## Requirements

- Go 1.25+ for this repository
- Goa v3 CLI: `go install goa.design/goa/v3/cmd/goa@latest`
- Optional for production: Temporal, MongoDB, Redis/Pulse

---

## Learn More

| Resource | Use it for |
| --- | --- |
| [`quickstart/README.md`](quickstart/README.md) | Copy-paste runnable project setup |
| [`docs/overview.md`](docs/overview.md) | Architecture and mental model |
| [`docs/dsl.md`](docs/dsl.md) | Complete DSL reference and patterns |
| [`docs/runtime.md`](docs/runtime.md) | Runtime API, planners, engines, stores, streaming, policies |
| [`DESIGN.md`](DESIGN.md) | Generator design and repository architecture |
| [Goa-AI docs](https://goa.design/docs/2-goa-ai/) | Published guides |
| [Go package docs](https://pkg.go.dev/goa.design/goa-ai) | API reference |

---

## Contributing

Issues and PRs are welcome. Include a Goa design, a failing test, or a clear reproduction when reporting behavior. See [`AGENTS.md`](AGENTS.md) for repository guidelines.

## License

MIT License (C) Raphael Simon and the [Goa community](https://goa.design).

<p align="center">
  <i>Build agent systems with contracts you can read, code you can trust, and runtime behavior you can operate.</i>
</p>
