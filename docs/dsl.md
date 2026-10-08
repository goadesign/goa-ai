# Goa Agent DSL Reference

This document explains how to author agents, toolsets, and runtime policies with the Goa‑AI DSL.
Use it alongside `docs/overview.md` and `docs/runtime.md` for the broader architecture and runtime
details.

## Overview

- **Import path:** `"goa.design/goa-ai/dsl"` (typically dot-imported alongside Goa's DSL).
- **Entry point:** Declare agents inside a regular Goa `Service` definition. The DSL augments Goa's
design tree and is processed during `goa gen`.
- **Outcome:** `goa gen` produces agent packages (`gen/<service>/agents/<agent>`), service-owned
completion packages (`gen/<service>/completions`), tool codecs/specs, activity handlers, and
registration helpers. A contextual `AGENTS_QUICKSTART.md` is written at the module root unless
disabled via `DisableAgentDocs()`.

The DSL is evaluated by Goa's `eval` engine, so the same rules apply as with the standard
service/transport DSL: expressions must be invoked in the proper context, and attribute definitions
reuse Goa's type system (`Attribute`, `Field`, validations, examples, etc.).

## Quickstart

```go
package design

import (
	. "goa.design/goa/v3/dsl"
	. "goa.design/goa-ai/dsl"
)

var DocsToolset = Toolset("docs.search", func() {
	Tool("search", "Search indexed documentation", func() {
		Args(func() {
			Attribute("query", String, "Search phrase")
			Attribute("limit", Int, "Max results", func() { Default(5) })
			Required("query")
		})
		Return(func() {
			Attribute("documents", ArrayOf(String), "Matched snippets")
			Required("documents")
		})
		Tags("docs", "search")
	})
})

var AssistantSuite = Toolset(FromMCP("assistant", "assistant-mcp"))

var _ = Service("orchestrator", func() {
	Description("Human front door for the knowledge agent.")

	Agent("chat", "Conversational runner", func() {
		Use(DocsToolset)
		Use(AssistantSuite)
		Export("chat.tools", func() {
			Tool("summarize_status", "Produce operator-ready summaries", func() {
				Args(func() {
					Attribute("prompt", String, "User instructions")
					Required("prompt")
				})
				Return(func() {
					Attribute("summary", String, "Assistant response")
					Required("summary")
				})
				Tags("chat")
			})
		})
		RunPolicy(func() {
			DefaultCaps(
				MaxToolCalls(8),
				MaxRecoveryTurns(3),
			)
			TimeBudget("2m")
			OnMissingFields("await_clarification")
		})
	})
})
```

Running `goa gen example.com/assistant/design` produces:

- `gen/orchestrator/agents/chat`: workflow + planner activities + agent registry.
- `gen/orchestrator/agents/chat/specs`: payload/result structs, JSON codecs, tool schemas.
- `gen/orchestrator/agents/chat/agenttools`: helpers that expose exported tools to other agents.
- `gen/orchestrator/completions`: typed direct-assistant completion specs/codecs/helpers when the
service declares `Completion(...)`.
- MCP registration helpers when an MCP toolset is referenced via `Use`.

Each per-toolset specs package defines typed tool identifiers (`tools.Ident`) and uses those
constants inside the exported `Specs` slice:

```go
const (
    Search tools.Ident = "orchestrator.search.search"
)

var Specs = []tools.ToolSpec{
    { Name: Search, /* ... */ },
}
```

Use these constants anywhere you need to reference tools.

### Service-Owned Typed Completions

Some structured interactions are direct assistant responses rather than tool calls.
Use `Completion(...)` inside a Goa `Service` to declare a typed assistant-output
contract owned by the service itself:

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

`Completion(...)` reuses Goa’s normal type system, so `Attribute`, `Required`,
`Example`, validations, and `OneOf` all apply exactly as they do for tool payloads
and results.

For tools, a top-level Goa `Example(...)` on the `Args` attribute is the only
source promoted to provider-facing tool examples. Codegen emits the annotated
JSON Schema, a second schema with only the root `example` removed, and a parsed
JSON-object `ExampleInput` at generation time. Provider adapters then select the
precomputed projection: OpenAI-style providers use the annotated schema,
direct Anthropic and Claude-on-Vertex use top-level `input_examples` with the
plain root schema, and Claude through `bedrock.NewAnthropic` uses the annotated
schema because Bedrock Messages rejects `input_examples`. No provider receives
synthesized examples as top-level tool examples.

Generated JSON Schemas expose `OneOf` values through the same discriminated
envelope the codecs accept: `{ "type": "<variant>", "value": <typed-payload> }`.
Each schema variant fixes `type` to exactly one enum value and gives `value` the
variant payload schema. The description authored on each Goa `Field` is emitted
on that variant's schema branch, so models receive the meaning of each legal
choice alongside its shape. Model-facing contracts therefore do not advertise
untyped `anyOf` payloads or raw string shortcuts.

Completion names are part of the structured-output contract. They must be
1-64 ASCII characters, may contain letters, digits, `_`, and `-`, and must
start with a letter or digit.

Running `goa gen` emits a service-owned package under `gen/<service>/completions`
that contains:

- completion result types and unions
- canonical root examples explicitly authored with Goa `Example(...)`
- generated JSON codecs and validation helpers
- generated `Spec<Name>()` factories that return fresh typed contracts with the
  schema, example, and codec
- generated `Complete<Name>(ctx, client, req)` helpers that request provider-enforced
structured output and decode the assistant response through the generated codec
- generated typed `StreamComplete<Name>(ctx, client, req)` helpers
- narrow `<Name>Example()` accessors that return an isolated copy when a caller
  needs the authored example

Use `Complete<Name>(...)` or `StreamComplete<Name>(...)` for provider-enforced
structured output. Use the fresh `Spec<Name>()` result with generic completion
helpers or `runtime/agent/tooloutput.Run` when the same generated contract must
run through an ordinary forced tool. Callers that only need an authored example
use `<Name>Example()`.

Provider adapters send an authored root example through the provider's native
structured-output example field when available. If the generated codec rejects
model-authored JSON, unary helpers return a non-retryable
`planner.OutputContractError` and do not ask the model again.
`completion.Response.ModelResponse` retains that exact model response and its
token usage.

Streaming helpers return a typed `completion.Streamer[T]`. Providers may emit
preview `completion_delta` chunks, but `Value` remains unavailable until the
stream ends normally and its final `completion` chunk matches the complete
response. Streaming never restarts after exposing output.

### Agent‑as‑Tool Composition (Child Workflows)

When agent A "uses" a toolset exported by agent B, Goa‑AI wires composition automatically:

- The exporter (agent B) package includes a generated `agenttools` package with typed tool IDs and
`NewRegistration(definition, systemPrompt, ...runtime.AgentToolOption)` helpers.
- The consumer registers the returned `runtime.ToolsetRegistration` with its runtime. The consumer
does not need the exporter’s planner locally; it only needs routing metadata.
- At runtime, invoking an exported tool starts the exporter agent as a **child workflow** using the
generated route metadata. The parent emits `ChildRunLinked` and the returned `ToolResult`
includes a `RunLink` handle to the child run.

Each run has its own event stream. Stream profiles select which event kinds are emitted to
different audiences and link child runs via run handles rather than flattening run identity.

---

## Prompt Management in v1

Goa-AI v1 does **not** require an agent prompt declaration DSL. The MCP `Prompt` declaration below exposes service-owned messages to an MCP client; it does not configure the agent prompt registry.
Prompt management is intentionally runtime-driven:

- Register baseline prompt specs via `Runtime.PromptRegistry.Register(prompt.PromptSpec{...})`.
- Configure scoped overrides with `runtime.WithPromptStore(...)` (for example, Mongo prompt store).
- Rendering returns text and prompt identity but never writes runtime storage.
  When a `prompt.RenderRecorder` is present, each successful render adds the
  same ID, version, and scope event used by every runtime path.
- Render prompts in planners using `PlannerContext.RenderPrompt(...)`; the
  runtime carries recorded events through the accepted planner result.
- If application code renders text before `Start`, create a recorder with
  `prompt.NewRenderRecorder`, attach it with `prompt.WithRenderRecorder`, and
  pass `recorder.Events()` through `runtime.WithRenderedPrompts(...)` alongside
  the messages containing that text.
- For agent-as-tool registrations, consumer-side prompt rendering is optional. If you need the
  consumer to render a payload-only user message, you may map tool IDs to prompt IDs with
  `runtime.WithPromptSpec(...)` (or provide templates/text). When no consumer-side content is
  configured, the runtime uses the canonical JSON tool payload bytes as the nested user message,
  and provider planners can render their own prompts with injected server-side context. Consumer-side
  rendering records the same event and carries it in the child workflow input. `RunOneShot` also uses
  the same recorder contract for renders performed by its callback.

This keeps prompt rollout and overrides operational (runtime/store level) while the DSL remains focused
on agent/tool contracts.

---

## Function Reference

### Agent Functions


| Function                               | Context                     | Purpose                                                         |
| -------------------------------------- | --------------------------- | --------------------------------------------------------------- |
| `Agent(name, description, dsl)`        | Inside `Service`            | Declares an LLM agent with tool usage/exports and run policy    |
| `Completion(name, description?, dsl?)` | Inside `Service`            | Declares a service-owned typed direct assistant-output contract |
| `Use(value, dsl?)`                     | Inside `Agent`              | Declares toolset consumption (referencing or inline definition) |
| `Export(value, dsl?)`                  | Inside `Agent` or `Service` | Declares toolsets exposed to other agents                       |
| `AgentToolset(svc, agent, ts)`         | Top-level or inside `Use`   | References a toolset exported by another agent                  |
| `DisableAgentDocs()`                   | Inside `API`                | Disables `AGENTS_QUICKSTART.md` generation                      |
| `Passthrough(tool, target...)`         | Inside exported `Tool`      | Forwards tool execution to a Goa service method                 |


### Toolset Functions


| Function                            | Context                    | Purpose                                                                    |
| ----------------------------------- | -------------------------- | -------------------------------------------------------------------------- |
| `Toolset(args...)`                  | Top-level                  | Defines a provider-owned toolset                                           |
| `FromMCP(service, toolset)`         | Argument to `Toolset`      | Configures a Goa-defined MCP server in the same design as toolset provider |
| `FromExternalMCP(service, toolset)` | Argument to `Toolset`      | Configures an external MCP server with inline tool schemas                 |
| `FromRegistry(registry, toolset)`   | Argument to `Toolset`      | Configures registry as toolset provider                                    |
| `Tags(values...)`                   | Inside `Toolset` or `Tool` | Attaches metadata labels for categorization                                |


### Tool Functions


| Function                                      | Context                                | Purpose                                                                                             |
| --------------------------------------------- | -------------------------------------- | --------------------------------------------------------------------------------------------------- |
| `Tool(name, description?, dsl?)`              | Inside `Toolset` or `Method`           | Declares a callable tool                                                                            |
| `Args(type)`                                  | Inside `Tool`                          | Defines an object-shaped input schema                                                               |
| `Return(type)`                                | Inside `Tool` or `Completion`          | Defines the model-visible result schema                                                             |
| `ServerData(kind, type, dsl?)`                | Inside `Tool`                          | Defines server-only data emitted alongside results (never sent to model providers)                  |
| `ServerDataDefault("on"                       | "off")`                                | Inside `Tool`                                                                                       |
| `BindTo(method)` or `BindTo(service, method)` | Inside `Tool`                          | Binds tool to a Goa service method                                                                  |
| `Inject(fields...)`                           | Inside `Tool`                          | Marks fields as server-injected (hidden from LLM)                                                   |
| `CallHintTemplate(tmpl)`                      | Inside `Tool`                          | Go template for call display hint                                                                   |
| `ResultHintTemplate(tmpl)`                    | Inside `Tool`                          | Go template for result display hint                                                                 |
| `BoundedResult(dsl?)`                         | Inside `Tool`                          | Declares a runtime-owned bounded-result contract; optional sub-DSL can declare paging cursor fields |
| `Cursor(name)`                                | Inside `BoundedResult(func() { ... })` | Declares which payload field carries the paging cursor (optional)                                   |
| `ContinueWith(tool, cursor)`                  | Inside `BoundedResult(func() { ... })` | Delegates paging to a sibling continuation tool whose cursor is bound by the runtime                 |
| `NextCursor(name)`                            | Inside `BoundedResult(func() { ... })` | Declares the projected result field name for the next-page cursor (optional)                        |
| `ResultReminder(text)`                        | Inside `Tool`                          | Static result guidance in ordinary and text-only runs                                               |
| `UIResultReminder(text)`                      | Inside `Tool`                          | Static result guidance only when interactive output is supported                                    |
| `Confirmation(dsl)`                           | Inside `Tool`                          | Declares that tool execution must be explicitly approved out-of-band                                |
| `TerminalRun()`                               | Inside `Tool`                          | Marks tool as terminal: run completes immediately after execution                                   |
| `Bookkeeping()`                               | Inside `Tool`                          | Marks control-plane work that consumes no `MaxToolCalls` budget and does not force another planner turn |


### Tool payload defaults (Feature)

Tool payload defaults follow Goa’s request‑style semantics:

- Codecs decode JSON into helper “decode‑body” structs with pointer fields to distinguish **missing**
from **zero**.
- Codecs then transform helper → final payload using Goa’s transform generator, which injects
default values deterministically.
- As a result, optional primitive fields with defaults are emitted as **value fields** (non‑pointers)
in the final tool payload type.

See `[docs/tool_payload_defaults.md](tool_payload_defaults.md)` for the complete contract and the
generator invariants.

### Native image evidence

Inside `ServerData`, `NativeImage()` requires `AudienceEvidence()`. It marks the
generated typed data as a retained image descriptor, without exposing that data
in semantic result JSON. Generation emits `NativeImageSources()` for explicit
host producer/kind admission and preserves the marker in registry contracts.
The host verifies current access and immutable content on each actual read.
See [retained native images](native_images.md) before enabling the marker:
canonical-message consumers and historical kind decoders must be ready before
new source parts are saved.

### Bounded results (returned / total / truncated / refinement_hint)

`BoundedResult` exists so tools can return a bounded view (caps, window clamping,
downsampling, pagination) while Goa-AI exposes a single canonical bounds contract.
The semantic tool result remains domain-specific; the runtime contract lives in
`planner.ToolResult.Bounds`.

Canonical model-visible fields:

- `returned` (**required**, Int)
- `truncated` (**required**, Boolean)
- `total` (Int; optional when the provider cannot know exact cardinality,
  required when the service contract always knows it)
- `refinement_hint` (optional, String)
- `next_cursor` (optional, String) when declared via `NextCursor(...)`

`Cursor(...)`, `ContinueWith(...)`, and `NextCursor(...)` name Goa attributes or
sibling tools in the generated contract. Generated schemas,
`tools.ToolSpec.Bounds`, and runtime result JSON expose model-facing JSON names,
so a lower-camel attribute such as `nextCursor` is projected as `next_cursor`.

Single source of truth:

- `BoundedResult(...)` records the contract in generated `tools.ToolSpec.Bounds`.
- Codegen projects the canonical bounded fields into the generated JSON result schema.
- Successful bounded tool executions must set `planner.ToolResult.Bounds`.
- The runtime projects those bounds back into encoded tool-result JSON, result-hint
template data, hooks, and stream events.
- Runtime enforcement is strict across all ingress paths: if `truncated=true`,
bounds must include either `next_cursor` or `refinement_hint`.

Tool-facing return types therefore must not duplicate canonical bounded fields just
so the model can see them. Validation rejects authored `Return(...)` shapes that
declare `returned`, `total`, `truncated`, `refinement_hint`, or the configured
`next_cursor` field. Keep authored result types semantic and domain-focused.

For method-backed `BindTo` tools, the bound service method result may still carry
the canonical bounded fields so the generated executor can build
`planner.ToolResult.Bounds`. `returned` and `truncated` are required. `total`
may be required when the method always computes exact cardinality; otherwise it
remains optional. `refinement_hint` and `next_cursor` remain optional.

### Pagination (cursor / next_cursor)

Tools that return potentially large datasets should support cursor-based pagination when practical.
Cursor-paged tools identify two canonical paging fields:

- **payload cursor field** (declared via `Cursor("field_name")`)
- **projected result next-cursor field** (declared via `NextCursor("field_name")`)

Contract:

- Treat cursors as **opaque**: do not parse, modify, or synthesize them.
- Prefer `ContinueWith("continue_tool", "cursor")` when the cursor identifies the
  prior query. The sibling continuation declares the required cursor with
  `Cursor("cursor")`; its model-facing schema is an empty object. The runtime
  advances zero-item pages automatically because they contain no evidence for
  the model to judge. After a page returns items, the runtime advertises one
  temporary action per live result-chain head, with the original model-visible
  query in the action description. The model may choose and call independent
  actions in one batch. The runtime preserves each empty model payload for
  replay and binds the selected source tool-call identity, cursor, and any
  required prior query fields only in the execution payload. Every continuation
  must return a cursor different from the one it consumed.
- Use `Cursor(...)` directly on the original tool only when the caller must
  intentionally repeat all other query arguments. In that mode, keep those
  arguments unchanged and set only the cursor field.
- Paged tools should also be `BoundedResult(...)` tools and return the next cursor through
`planner.ToolResult.Bounds.NextCursor`.

### Tool Confirmation (Human-in-the-Loop)

Some tools represent **irreversible** or **operator-sensitive** actions (writes, deletes, commands).
Use `Confirmation` to declare that a tool must be approved out-of-band before execution.

At code generation time, Goa-AI records the confirmation policy in the generated `tools.ToolSpec`.
At runtime, the workflow emits a confirmation `AwaitConfirmation` request and only executes the tool
after an explicit approval is provided.

Example:

```go
Tool("dangerous_write", "Write a stateful change", func() {
    Args(DangerousWriteArgs)
    Return(DangerousWriteResult)
    Confirmation(func() {
        Title("Confirm change")
        PromptTemplate(`Approve write: set {{ .key }} to {{ json .value }}`)
        DeniedResultTemplate(`{"summary":"Cancelled","key":{{ json .key }}}`)
    })
})
```

Notes:

- The runtime owns how confirmation is requested. The built-in protocol ends
  the current workflow with a dedicated `AwaitConfirmation` request. The next
  workflow starts with the returned suspension and one typed confirmation
  decision. See `docs/runtime.md` for the expected payloads and flow.
- Templates read canonical JSON property names, such as `{{ .key }}`, rather than generated Go field names. Use `index` for optional properties.
- Confirmation templates (`PromptTemplate` and `DeniedResultTemplate`) are Go `text/template` strings
executed with `missingkey=error`. In addition to the standard template functions (e.g. `printf`),
Goa-AI provides:
  - `json v` → JSON encodes `v` without manual quoting or number conversion.
  - `quote s` → returns a Go-escaped quoted string (like `fmt.Sprintf("%q", s)`).
- Design-time confirmation is the **common case** (“this tool always needs approval”), but runtimes
can also require confirmation dynamically for additional tools via `runtime.WithToolConfirmation(...)`
(see runtime docs).

### Policy Functions


| Function                           | Context                   | Purpose                                                                      |
| ---------------------------------- | ------------------------- | ---------------------------------------------------------------------------- |
| `RunPolicy(dsl)`                   | Inside `Agent`            | Configures runtime execution constraints                                     |
| `DefaultCaps(opts...)`             | Inside `RunPolicy`        | Sets resource limits using option functions                                  |
| `MaxToolCalls(n)`                  | Argument to `DefaultCaps` | Maximum budgeted (non-bookkeeping) tool invocations                          |
| `MaxRecoveryTurns(n)` | Argument to `DefaultCaps` | Maximum consecutive replacement planner activities after rejected tool or model output |
| `TimeBudget(duration)`             | Inside `RunPolicy`        | Active-time budget for planner and tool work (e.g., "5m")                    |
| `OnMissingFields(action)`          | Inside `RunPolicy`        | Validation behavior: `""`, `"finalize"`, `"await_clarification"`, `"resume"` |


### Timing Functions


| Function           | Context                        | Purpose                                |
| ------------------ | ------------------------------ | -------------------------------------- |
| `Timing(dsl)`      | Inside `RunPolicy`             | Groups timing configuration            |
| `Budget(duration)` | Inside `RunPolicy` or `Timing` | Active-time budget for planner and tool work |
| `Plan(duration)`   | Inside `RunPolicy` or `Timing` | Timeout for Plan and Resume activities |
| `Tools(duration)`  | Inside `RunPolicy` or `Timing` | Default timeout for tool activities    |


### History Functions


| Function                         | Context            | Purpose                                                           |
| -------------------------------- | ------------------ | ----------------------------------------------------------------- |
| `History(dsl)`                   | Inside `RunPolicy` | Configures conversation history management                        |
| `KeepRecentTurns(n)`             | Inside `History`   | Retain only the most recent N turns without summarization         |
| `CompressAtTurns(n)`             | Inside `History`   | Summarize older turns once at least N logical turns are present   |
| `CompressAtMaxInputTokens(n)`    | Inside `History`   | Summarize older turns when runtime input-token count exceeds N    |
| `KeepMaxTurns(n)`                | Inside `History`   | Keep at most N newest complete turns exact after summarization    |
| `KeepMaxInputTokens(n)`          | Inside `History`   | Keep newest complete turns whose runtime token count fits budget  |

Compression requires at least one `CompressAt...` trigger and at least one
`KeepMax...` retention budget. Token budgets are defaults; generated agent
config can replace them at runtime for the deployed model. When either trigger
or retention uses tokens, the destination model's counter measures the actual
request. `HistoryModel` only writes summaries. Counts must be exact unless
`HistoryCompressionConfig.AllowEstimatedTokens` explicitly permits estimates;
an estimate is not a context-window guarantee or a billing count.


### Cache Functions


| Function        | Context            | Purpose                                       |
| --------------- | ------------------ | --------------------------------------------- |
| `Cache(dsl)`    | Inside `RunPolicy` | Configures prompt caching hints               |
| `AfterSystem()` | Inside `Cache`     | Place cache checkpoint after system messages  |
| `AfterTools()`  | Inside `Cache`     | Place cache checkpoint after tool definitions |


### MCP Functions


| Function                            | Context                            | Purpose                                        |
| ----------------------------------- | ---------------------------------- | ---------------------------------------------- |
| `MCP(name, version)`       | Inside `Service`                   | Enables MCP protocol for the service           |
| `Tool(name, description)`           | Inside `Method` (with MCP enabled) | Marks method as MCP tool                       |
| `ToolContent(field)` | Inside an MCP method's `Tool` block | Sends a typed result array as MCP content, excluding it from structured JSON |
| `Resource(name, uri, mime)`         | Inside `Method`                    | Marks method as MCP resource provider          |
| `Prompt(name, description)` | Inside `Method` (with MCP enabled) | Exposes typed, parameterized MCP messages |
| `StaticPrompt(name, desc, msgs...)` | Inside `Service` (with MCP)        | Defines static MCP prompt template             |

Every service that calls `MCP(...)` must also use Goa's service-level
`JSONRPC(func() { POST("/path") })` DSL to choose its HTTP endpoint.


### Registry Functions


| Function                     | Context                                | Purpose                                   |
| ---------------------------- | -------------------------------------- | ----------------------------------------- |
| `Registry(name, dsl?)`       | Top-level                              | Declares a remote registry source         |
| `URL(url)`                   | Inside `Registry`                      | Sets the registry endpoint URL (required) |
| `APIVersion(version)`        | Inside `Registry`                      | Sets registry API version (default: "v1") |
| `Security(scheme)`           | Inside `Registry`                      | References Goa security scheme for auth   |
| `Timeout(duration)`          | Inside `Registry`                      | Sets HTTP request timeout                 |
| `Retry(maxRetries, backoff)` | Inside `Registry`                      | Configures retry policy                   |
| `SyncInterval(duration)`     | Inside `Registry`                      | Sets catalog refresh interval             |
| `CacheTTL(duration)`         | Inside `Registry`                      | Sets local cache duration                 |
| `Federation(dsl)`            | Inside `Registry`                      | Configures external registry imports      |
| `Include(patterns...)`       | Inside `Federation`                    | Glob patterns for namespaces to import    |
| `Exclude(patterns...)`       | Inside `Federation`                    | Glob patterns for namespaces to skip      |
| `PublishTo(registry)`        | Inside `Toolset` (in `Export`)         | Configures registry publication           |
| `Version(version)`           | Inside `Toolset` (with `FromRegistry`) | Pins toolset version                      |


---

## Agent, Use, and Export

### Agent

`Agent` declares an LLM-powered agent within a Goa service. Each agent becomes a runtime
registration with:

- A workflow definition and Temporal activity handlers
- PlanStart/PlanResume activities with DSL-derived retry/timeout options
- A `Register<Agent>` helper that registers workflows, activities, and toolsets

```go
Service("orchestrator", func() {
    Agent("chat", "Conversational assistant for operations", func() {
        Use(CommonTools)           // Consume toolsets
        Export("assistant", func() { // Export toolsets for other agents
            Tool("summarize", "Summarize conversation", func() { ... })
        })
        RunPolicy(func() { ... })  // Configure execution constraints
    })
})
```

### Use

`Use` declares that the current agent consumes a toolset. The value can be:

- A `*ToolsetExpr` returned by `Toolset`, `FromMCP`, or `FromExternalMCP` (provider-owned)
- A string name for an inline, agent-local toolset definition

An optional DSL function can:

- Subset tools from a referenced provider toolset by name
- Define ad-hoc tools local to this agent

```go
// Reference existing toolset
Use(CommonTools)

// Reference and subset
Use(CommonTools, func() {
    Tool("notify")  // Only consume the notify tool
})

// Inline definition
Use("adhoc", func() {
    Tool("custom_tool", "Agent-specific tool", func() {
        Args(func() { Attribute("input", String) })
        Return(func() { Attribute("output", String) })
    })
})
```

### Export

`Export` declares that the agent or service exports a toolset for other agents to consume.
Exported tools enable agent-as-tool composition where one agent can invoke another as a tool.

```go
Agent("specialist", "Domain expert", func() {
    Export("analysis", func() {
        Tool("deep_analyze", "Perform deep analysis", func() {
            Args(AnalysisRequest)
            Return(AnalysisResult)
        })
    })
})

// Consumer agent uses the exported toolset
Agent("orchestrator", "Main coordinator", func() {
    Use(AgentToolset("service", "specialist", "analysis"))
})
```

### Passthrough

`Passthrough` defines deterministic forwarding for an exported tool to a Goa service method.
Use it when an exported tool should directly invoke an existing service method without custom
executor logic.

```go
Export("logging-tools", func() {
    Tool("log_message", "Log a message", func() {
        Args(func() { Attribute("message", String) })
        Return(func() { Attribute("logged", Boolean) })
        Passthrough("log_message", "LoggingService", "LogMessage")
    })
})
```

---

## Toolset

### Basic Toolset Definition

`Toolset` declares a provider-owned group of related tools. When declared at top level, the
toolset becomes globally reusable; agents reference it via `Use` and services can expose it
via `Export`.

```go
var CommonTools = Toolset("common", func() {
    Description("Shared utility tools")
    Tags("utility", "common")
    Tool("notify", "Send notification", func() {
        Args(func() {
            Attribute("message", String, "Message to send")
            Attribute("channel", String, "Notification channel")
            Required("message")
        })
        Return(func() {
            Attribute("sent", Boolean, "Whether notification was sent")
        })
    })
})
```

### MCP-Backed Toolsets

MCP-backed toolsets now use explicit modes so the generator never has to infer
where schemas come from.

**Pattern 1: Goa-backed MCP server (same design)**

Use `FromMCP` when your MCP server is defined in the same design using the
`MCP(...)` DSL:

```go
Service("assistant", func() {
    MCP("assistant-mcp", "1.0.0")
    JSONRPC(func() {
        POST("/mcp")
    })
    Method("search", func() {
        Payload(SearchParams)
        Result(SearchResults)
        Tool("search", "Search documents")  // Mark as MCP tool
    })
})

// Reference the MCP toolset
var AssistantSuite = Toolset(FromMCP("assistant", "assistant-mcp"))

Agent("chat", "Chat agent", func() {
    Use(AssistantSuite)
})
```

`FromMCP` in this mode must point at a Goa service that declares `MCP(...)`, and
the toolset must not declare inline `Tool(...)` schemas.

**Pattern 2: External MCP server (inline schemas)**

Use `FromExternalMCP` for external MCP servers, and define the tool schemas
inline:

```go
var RemoteSearch = Toolset("remote-search", FromExternalMCP("remote", "search"), func() {
    Tool("web_search", "Search the web", func() {
        Args(func() { Attribute("query", String) })
        Return(func() { Attribute("results", ArrayOf(String)) })
    })
})

Agent("helper", "Helper agent", func() {
    Use(RemoteSearch)
})
```

`FromExternalMCP` requires at least one inline `Tool(...)` declaration because
those schemas are the contract surface for the external server.

At runtime, supply an `mcpruntime.Caller` for the toolset ID.
Agent and toolset names must produce a non-empty Go identifier during
evaluation.

### Registry-Backed Toolsets

`FromRegistry` configures a toolset to be sourced from a remote registry:

```go
var CorpRegistry = Registry("corp", func() {
    URL("https://registry.corp.internal")
    Security(CorpAPIKey)
})

var DataTools = Toolset(FromRegistry(CorpRegistry, "data-tools"))

// With version pinning
var PinnedTools = Toolset(FromRegistry(CorpRegistry, "data-tools"), func() {
    Version("1.2.3")
})
```

Consume a named registry toolset with `Use(DataTools)`, or consume every
currently registered toolset with `Use(CorpRegistry)`. Add `Deferred()` inside
that `Use` to load definitions through native model tool search:

```go
Agent("analyst", "Analyze company data.", func() {
    Use(DataTools, func() { Deferred() })
})
```

Generated agents resolve current contracts once per planning activity. Connect
an already-constructed clustered registry client and Pulse client with
`rt.RegisterRegistry("corp", registryClient, pulseClient)` before starting runs.
`Definition()` and `NewClient(rt)` perform no discovery and require no catalog
arguments. Dynamic execution is provided by the runtime.

A named source must exist and match any version pin. A whole-registry source
may be empty. Duplicate sources, overlapping whole/named consumption, inline
registry `Tool` declarations, and exporting a registry reference are rejected.
The provider owns each remote contract and publishes generated `ToolSchemas()`
records. Service tools support confirmation, pagination, and server-only data;
agent and control integration remain compiled.

See [Tool search and dynamic registries](tool_search.md) for provider behavior,
connection wiring, selected-call persistence, catalog changes, and upgrades
from startup `Discover`/`RegistryToolsets` integration.

### Deferred tools

`Deferred()` belongs inside a consuming `Use` for a compiled toolset, a named
registry toolset, or a whole registry. It changes how the model loads tool
definitions without changing which tools the agent may call. Another consumer
of the same toolset can load it immediately. Generated code owns each agent's
loading choice and prepares search terms from canonical tool metadata.

For a compiled toolset, use `Deferred("search", "analyze")` to defer only those
exact authored local tool names; other tools remain immediately available.
Named selection also supports declared external MCP and Goa-backed MCP tools.
It is rejected for `FromRegistry` toolsets and whole registries, whose tools are
resolved at runtime. Empty, duplicate, unknown, and mixed all/named selections
are errors; repeated `Deferred()` remains valid. See
[tool selection rules](tool_search.md#declare-what-the-agent-consumes) for details.

Planners pass `input.Agent.AdvertisedToolDefinitions()` to model requests.
The adapter handles native search; planners need no search executor. An adapter
that cannot implement the requested discovery rejects it explicitly. See the
[provider behavior table](tool_search.md#provider-behavior).

---

## Tool

Each `Tool` describes a callable capability. Tools can be defined inside toolsets for agent
consumption, or inside methods for MCP exposure.

### Tool in Toolset Context

Define the tool's schema inline with `Args` and `Return`:

```go
Toolset("utils", func() {
    Tool("summarize", "Summarize a document", func() {
        Args(func() {
            Attribute("text", String, "Document text to summarize")
            Attribute("max_length", Int, "Maximum summary length", func() {
                Minimum(10)
                Maximum(1000)
                Default(200)
            })
            Required("text")
        })
        Return(func() {
            Attribute("summary", String, "Summarized text")
            Attribute("word_count", Int, "Word count of summary")
            Required("summary")
        })
        Tags("nlp", "summarization")
    })
})
```

### Tool in Method Context (MCP)

When used inside a Method with MCP enabled, `Tool` marks the method as an MCP tool.
The method's payload becomes the tool input schema and the result becomes the output schema:

```go
Service("calculator", func() {
    MCP("calc", "1.0.0")
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
            Attribute("sum", Int, "Sum of the numbers")
        })
        Tool("add", "Add two numbers")  // Marks method as MCP tool
    })
})
```

### Args and Return

`Args` defines the model-visible input object for a tool. It accepts an inline
object or an object-shaped user type. Primitive, array, map, and `OneOf` roots
are rejected; wrap those values in an object instead.
Omitting `Args` on an unbound tool defines the empty object `{}`, never `null`.
On a tool with `BindTo`, omitting `Args` uses the bound method payload, which
must also be object-shaped. See the
[runtime tool-input contract](runtime.md#model-visible-tool-arguments) for
nested object, union, validation, and correction behavior.

```go
// Inline schema
Args(func() {
    Attribute("query", String, "Search query")
    Attribute("limit", Int, "Max results")
    Required("query")
})

// Reuse existing type
Args(SearchParams)

// Wrap a primitive value in an object
Args(func() {
    Attribute("text", String, "Text to echo")
    Required("text")
})
```

`Return` is unchanged and accepts the same result shapes as Goa's `Result`:

```go
// Inline schema
Return(func() {
    Attribute("results", ArrayOf(Document))
    Attribute("total", Int)
    Required("results")
})

// Reuse existing type
Return(SearchResults)

// Primitive type
Return(Int)  // Single integer return
```

### ServerData (Non-Model Data)

`ServerData` defines structured data attached to tool results that is **not** sent to the model
provider. Use it for full-fidelity data that backs a bounded model-facing result, or for
server-only metadata that must be persisted alongside tool executions.

```go
Tool("get_time_series", "Get time series data", func() {
    Args(GetTimeSeriesArgs)
    Return(GetTimeSeriesReturn)           // Model sees this (summary/bounded view)
    ServerData("charts.time_series", TimeSeriesData) // Full data for UI/downstream only
    ServerDataDefault("off")                        // Opt-in by default
})
```

Server-data is never included in prompts to the LLM. Optional server-data may be projected into
observer-facing projections via `planner.ToolResult.ServerData`, hooks, and stream events while
always-on server-data is intended for in-process subscribers such as persistence and telemetry.
The registry executor decodes every returned item with the generated codec for its declared kind
and re-encodes canonical JSON before the runtime records it. Unknown or duplicate kinds, audience
mismatches, and schema-invalid data fail the tool result as malformed; they are never persisted as
best-effort observer data.

#### Declaring a server-data audience (`Audience*`)

Each `ServerData` entry declares an *audience* that downstream consumers use to route the payload
without relying on kind naming conventions:

- `"timeline"`: persisted and eligible for observer-facing projection (e.g., timeline/UI cards)
- `"internal"`: tool-composition attachment; not persisted or rendered
- `"evidence"`: provenance references; persisted separately from timeline cards

Use the DSL helpers inside the `ServerData` block:

```go
Tool("get_time_series", "Get time series data", func() {
    // ...
    ServerData("charts.time_series.points", TimeSeriesChartPoints, func() {
        Description("Chart-oriented points for downstream tool composition.")
        AudienceInternal()
        FromMethodResultField("ChartSidecar")
    })
    ServerData("records.evidence", ArrayOf(Evidence), func() {
        Description("Provenance references for server-side persistence.")
        AudienceEvidence()
        ModeAlways()
        FromMethodResultField("Evidence")
    })
})
```

#### Controlling optional server-data emission (`server_data` + `ServerDataDefault`)

Tools that declare optional `ServerData` automatically accept a reserved payload field named `server_data`
with values `"auto"`, `"on"`, and `"off"`:

- `"on"`: enable optional server-data for this call.
- `"off"`: suppress optional server-data for this call.
- `"auto"` (or omitted): use the tool’s default behavior.

Use `ServerDataDefault("off")` inside the tool DSL to make optional server-data opt-in by default when
`"auto"` or omission is used. This is useful for tools whose observer-facing output is only appropriate
when the user explicitly asked for a visualization.

#### Always-on server-data (server-side persistence)

When you need server-only metadata that must be emitted/persisted regardless of `server_data` toggles,
declare an always-on ServerData entry:

```go
Tool("get_time_series", "Get time series data", func() {
    // ...
    ServerData("records.evidence", func() {
        ModeAlways()
        FromMethodResultField("Evidence")
    })
})
```

### BindTo (Service Method Binding)

`BindTo` associates a tool with a Goa service method implementation. This enables tools to
reuse existing service logic with generated transforms between tool and method types.

```go
Service("docs", func() {
    Method("search_documents", func() {
        Payload(func() {
            Attribute("query", String)
            Attribute("session_id", String)  // Infrastructure field
            Required("query", "session_id")
        })
        Result(func() {
            Attribute("documents", ArrayOf(Document))
        })
    })
    
    Agent("assistant", "Document assistant", func() {
        Use("doc-tools", func() {
            Tool("search", "Search documents", func() {
                Args(func() {
                    Attribute("query", String, "Search query")
                    Required("query")
                })
                Return(func() {
                    Attribute("documents", ArrayOf(Document))
                })
                BindTo("search_documents")  // Bind to method in same service
                Inject("session_id")        // Hide from LLM, set at runtime
            })
        })
    })
})

// Cross-service binding
Tool("notify", "Send notification", func() {
    Args(func() { Attribute("message", String) })
    BindTo("notifications", "send")  // Different service
})
```

Codegen produces transform helpers when shapes are compatible:

- `Init<Tool>MethodPayload(in <ToolArgs>) <MethodPayload>`
- `Init<Tool>ToolResult(in <MethodResult>) <ToolReturn>`

### Inject (Server-Side Fields)

`Inject` marks payload fields as server-populated. Injected fields are:

1. Hidden from the LLM (excluded from the JSON schema and the model-facing required list)
2. Required `String` fields on the tool's effective payload (the explicit `Args()` when given, otherwise the bound method's payload). Named Goa `String` types are supported; fields that replace their Go type with `struct:field:type` are rejected because injection supplies a string value.
3. Populated by generated code, from one of two generation-time-resolved sources

**Meta-backed** names Goify to one of the five fixed `runtime.ToolCallMeta`
fields -- `run_id`/`runId`, `session_id`/`sessionId`, `turn_id`/`turnId`,
`tool_call_id`/`toolCallId`, `parent_tool_call_id`/`parentToolCallId` -- and
compile to a direct meta read. **Every other name is label-backed**: it
compiles to a run-label lookup (label key = the design name verbatim), with
the field's own declared validation (`Pattern`, `MinLength`, enum, and other
String rules) applied before assignment, and callers must supply it via
`runtime.WithLabels(...)` when starting the run. Metadata-backed values pass
through the same validation before assignment. Registry calls carry the run
labels needed by bound tools.

```go
Tool("get_data", "Get user data", func() {
    Args(func() {
        Attribute("user_id", String, "User to look up")
        Attribute("session_id", String, "Current session")
        Required("user_id", "session_id")
    })
    BindTo("data_service", "get_user")
    Inject("session_id")  // meta-backed: hidden from LLM, set by runtime
})

Tool("lookup_household", "Lookup scoped to a household", func() {
    Args(func() {
        Attribute("household_id", String, "Household to scope the search to.", func() {
            Pattern("^[a-z0-9-]+$")
        })
        Attribute("query", String, "Search query.")
        Required("household_id", "query")
    })
    Inject("household_id")  // label-backed: set via WithLabels("household_id", ...)
})
```

Codegen emits one `Inject<Tool>` function per injecting tool (in the
toolset's generated `inject.go`), which both execution topologies call
identically. Custom (hand-written) `ToolCallExecutor`s should decode these
tools' payloads with the generated `Decode<Tool>` function instead of the
raw payload codec, so injection can never be silently skipped. See
[`docs/runtime.md`](runtime.md)'s "Injected Fields" section for the full
design→codegen→runtime flow, run-start `RequiredLabels` enforcement, and the
`Decode<Tool>` contract.

### Display Hint Templates

`CallHintTemplate` and `ResultHintTemplate` configure Go templates for UI display.
These templates are rendered by the runtime against typed Go values and surfaced
via hook + stream events as `DisplayHint` (call) and result previews (successful
results only). Tool errors surface through their error payloads, not result
templates.

```go
Tool("search", "Search documents", func() {
    Args(func() {
        Attribute("query", String)
        Attribute("limit", Int)
    })
    Return(func() {
        Attribute("count", Int)
        Attribute("results", ArrayOf(String))
    })
    CallHintTemplate("Searching for: {{ .Query }} (limit: {{ .Limit }})")
    ResultHintTemplate("Found {{ .Result.Count }} results for {{ .Args.Query }}")
})
```

Templates are compiled with `missingkey=error`. Keep hints concise (≤140 characters recommended).
Template variables use Go field names, not JSON keys.

- Call templates receive the typed payload as the template root (for example,
`.Query`, `.Limit`).
- Result templates receive an explicit wrapper where payload fields live under
`.Args`, semantic fields live under `.Result`, and bounded metadata lives
under `.Bounds` (for example, `.Args.Query`, `.Result.Count`,
`.Bounds.Returned`). `.Args` is nil when the runtime cannot decode the
original tool call payload, and `.Bounds` is nil when the tool result is
unbounded.

**Runtime contract:**

- Tool call scheduled events default to `DisplayHint==""` at construction time. The runtime enriches and
persists a **durable default** hint from the typed template when payload decoding succeeds.
- If you set `DisplayHint` explicitly (non-empty) before publishing the hook event, the runtime treats it
as authoritative and will not overwrite it.
- Tool registration requires a non-empty metadata title. If typed decoding fails, the runtime uses that title
as the hint. The malformed payload still fails at the tool boundary; the metadata title only preserves the
display contract.

**Per-consumer overrides (optional):**

If you need a different hint for a specific deployment/consumer (e.g., UI wording), configure a runtime
override via `runtime.WithHintOverrides`. Overrides take precedence over DSL templates for streamed
`tool_start` events.

### BoundedResult

`BoundedResult` marks a tool's result as a bounded view over potentially larger data. When set:

- Generated `tools.ToolSpec.Bounds` describes the bounded-result contract
- Generated JSON result schemas include the canonical bounded fields
- Runtime requires `planner.ToolResult.Bounds` and attaches those bounds to hook events and streams

```go
Tool("list_devices", "List devices in scope", func() {
    Args(ListDevicesArgs)
    Return(ListDevicesReturn)
    BoundedResult(func() {
        ContinueWith("continue_devices", "cursor")
        NextCursor("next_cursor")
    })
})

Tool("continue_devices", "Continue listing devices", func() {
    Args(ContinueDevicesArgs) // one required cursor field
    Return(ListDevicesReturn)
    BoundedResult(func() {
        Cursor("cursor")
        NextCursor("next_cursor")
    })
})
```

Services and finalizers are responsible for trimming and populating
`planner.ToolResult.Bounds`. Goa-AI then projects those bounds into the
model-visible result JSON using model-facing field names derived from
`BoundedResult(...)`. Result-hint templates access the same runtime metadata via
`.Bounds`; Goa-AI does not merge those fields into the semantic result value.
For `ContinueWith`, the runtime exposes each unfinished query as a distinct
temporarily available no-argument action and binds the generated continuation
payload from that query's exact successful page in the result history.

### Tags

`Tags` attaches flat labels to tools or toolsets for generic policy and UI
categorization. Toolset tags are inherited by their tools.

```go
Tool("delete_file", "Delete a file", func() {
    Args(func() { Attribute("path", String) })
    Tags("filesystem", "write", "destructive")
})

Toolset("admin-tools", func() {
    Tags("admin", "privileged")
    Tool("reboot", "Reboot server", func() { ... })
})
```

Common tag patterns include:

- Domain: `"nlp"`, `"database"`, `"api"`, `"filesystem"`
- Capability: `"read"`, `"write"`, `"search"`, `"transform"`
- Risk: `"safe"`, `"destructive"`, `"external"`

### Meta

Goa's standard `Meta(name, values...)` DSL attaches a named design-time
annotation to the current tool. Goa-AI code generation preserves it in
`ToolSpec.Meta` as `map[string][]string`.

```go
Tool("resolve_source", "Resolve a selectable source", func() {
    Args(ResolveSourceArgs)
    Return(ResolveSourceResult)
    Meta("example.chat.supplies_tool_input")
})
```

Use `Meta` for a stable, consumer-owned contract that is not a generic policy
category. Metadata is inert: Goa-AI does not change scheduling, visibility,
budgets, retries, or terminal behavior merely because a key is present. The
planner, policy, or UI that interprets a key owns and documents its semantics.

Keep the built-in contracts canonical:

- use `Tags` for generic allow/deny and capability filtering,
- use `Bookkeeping` and `TerminalRun` for accounting and terminal behavior,
- use `ToolFailure` for per-result failure classification and recovery,
- use planner fields such as `SynthesizeAfterTools` for per-batch transitions.

### ResultReminder

`ResultReminder` configures a static system reminder that is injected into the conversation
after the tool result is returned. Use this to provide backstage guidance to the model about
how to interpret or present the result to the user. This guidance remains in
text-only runs. Declare guidance that assumes interactive output with
`UIResultReminder`; ordinary contracts combine both reminders with a newline.

The reminder text is automatically wrapped in `<system-reminder>` tags by the runtime. Do not
include the tags in the text.

This DSL function is for static, design-time reminders that apply every time the tool is
called. For dynamic reminders that depend on runtime state or tool result content, use
`PlannerContext.AddReminder()` in your planner implementation instead. Dynamic reminders
support rate limiting, per-run caps, and can be added or removed based on runtime conditions.

```go
Tool("get_time_series", "Get Time Series", func() {
    Args(GetTimeSeriesToolArgs)
    Return(GetTimeSeriesToolReturn)
    ResultReminder("Report the data with its time range.")
    UIResultReminder("The user sees a rendered graph of this data in the UI.")
})
```

### TerminalRun

`TerminalRun` marks a tool as terminal for the run: once it executes, the runtime
terminates the run immediately after publishing the tool result(s) without
requesting a follow-up PlanResume/finalization turn. Use it for tools whose
result is the user-facing terminal output of the run (for example, a final
report renderer or a commit-this-run tool).

```go
Tool("tasks_complete", "Commit the terminal task artifact", func() {
    Args(TaskCompletionArgs)
    Return(TaskCompletionResult)
    TerminalRun()
})
```

### Bookkeeping

`Bookkeeping` marks a tool as a control-plane record whose success does not
independently schedule another planner turn.

Runtime contract:

- bookkeeping calls do not consume the run-level `MaxToolCalls` retrieval budget,
- successful bookkeeping results do not reset the recovery-turn counter,
- model-authored call batches are admitted or rejected atomically, with
  bookkeeping calls excluded from the batch's budget cost,
- bookkeeping results still publish durable run events for hooks, streams, and
  run-log consumers,
- all bookkeeping calls and results remain in the provider transcript so signed
  responses replay unchanged,
- successful bookkeeping results stay out of compact future `ToolOutputs` and
  do not force another planner turn.

This means a successful bookkeeping-only planner turn is only valid when the same turn
already resolves without another reasoning resume (a `TerminalRun` tool, a
`FinalResponse` / `FinalToolResult`, or an external-input suspension). Every
failed bookkeeping result remains planner-visible and resumes
through its typed recovery transition. `correct_call` and `replan` may use
tools; `finish` resumes without tools so the planner can synthesize the
terminal outcome.

Operationally, a planner result is processed as one workflow step: the runtime
executes admitted tool and await work, records durable and planner-visible
outputs through one canonical path, then applies a single transition policy for
resume, finish, terminal-tool completion, or forced finalization. A
`FinalResponse` or `FinalToolResult` may only accompany hidden, non-terminal
bookkeeping calls that complete successfully in the same step.

For ordinary `correct_call` recovery, the runtime retains the current agent's
tools plus exact executable contracts for selected failed calls, after applying
the same caller and tag policy. It attaches full errors, generated validation
issues, rejected input, and examples. The planner may retry, choose another
authorized action, continue an unfinished query, await input, or answer.
Finalization still permits only correction of its failed terminal tool.
`replan` removes its
failed tool for the recovery activity unless another selected failure for that
same tool is correctable. Caller-supplied `WithRestrictToTool` policy remains
run-scoped.

```go
Tool("set_step_status", "Update step status", func() {
    Args(SetStepStatusArgs)
    Return(SetStepStatusResult)
    Bookkeeping()
})
```

`TerminalRun` implies `Bookkeeping`: a terminal commit consumes no tool-call
budget and completes the run when it succeeds. Declare
`TerminalRun()` alone; the DSL supplies the bookkeeping classification.

---

## RunPolicy, Caps & History

`RunPolicy` configures execution limits and behavior enforced at runtime:

```go
RunPolicy(func() {
    // Resource limits
    DefaultCaps(
        MaxToolCalls(20),
        MaxRecoveryTurns(3),
    )
    
    // Timing
    TimeBudget("5m")
    Timing(func() {
        Budget("10m")   // Active planner and tool work
        Plan("45s")     // Planner activity timeout
        Tools("2m")     // Default tool timeout
    })
    
    // Missing model-authored tool fields may request user clarification.
    OnMissingFields("await_clarification")
    
    // History management
    History(func() {
        KeepRecentTurns(20)
    })
    
    // Prompt caching
    Cache(func() {
        AfterSystem()
        AfterTools()
    })
})
```

### DefaultCaps Options


| Option                             | Purpose                                |
| ---------------------------------- | -------------------------------------- |
| `MaxToolCalls(n)`                  | Maximum budgeted (non-bookkeeping) tool invocations per run |
| `MaxRecoveryTurns(n)` | Allow N consecutive replacement planner activities |


### OnMissingFields Values


| Value                   | Behavior                                        |
| ----------------------- | ----------------------------------------------- |
| `""` (empty)            | Let the planner decide based on context         |
| `"finalize"`            | Stop execution when required fields are missing |
| `"await_clarification"` | Pause and wait for user input                   |
| `"resume"`              | Continue execution despite missing fields       |


### History Policies

History policies transform the messages in an actual model request immediately
before the runtime sends it, preserving system prompts and logical turn boundaries.
One turn contains a complete contiguous assistant response and its tool results,
not each message fragment or an entire autonomous run. A user request stays with
its first response; subsequent completed response/result exchanges can be
retained or summarized independently. See [complete history turns](runtime.md#complete-history-turns)
for parallel calls, accompanying result text, reminders, and pending requests.
Planners read saved history from `Messages`; inspecting it or deciding an action
without calling a model performs no history counting or summarization.
See [the runtime preparation contract](runtime.md#preparing-conversation-messages)
for lifetime, error handling, and custom planner migration.

**Sliding Window** — Keep the last N turns:

```go
RunPolicy(func() {
    History(func() {
        KeepRecentTurns(20)
    })
})
```

**Compression** — Summarize older turns with a model:

```go
RunPolicy(func() {
    History(func() {
        CompressAtMaxInputTokens(120_000)
        KeepMaxInputTokens(40_000)
        KeepMaxTurns(12)
    })
})
```

When compression is configured, the generated agent config includes a
`HistoryModel` field that callers must supply with a `model.Client`. The runtime
uses `ModelClassSmall` for summarization and, when token budgets are set, counts
the pending request through its destination model's counter. The summary model
does not choose how another model's input is measured.

### Cache Policies

Cache policies configure prompt caching hints for providers that support it:

```go
RunPolicy(func() {
    Cache(func() {
        AfterSystem()  // Cache checkpoint after system messages
        AfterTools()   // Cache checkpoint after tool definitions
    })
})
```

### Timing Configuration

Fine-grained timeout control:

```go
RunPolicy(func() {
    Timing(func() {
        Budget("10m")  // Active planner and tool work
        Plan("45s")    // Timeout for Plan/Resume activities
        Tools("2m")    // Default timeout for tool activities
    })
})
```

`Timing` is intentionally semantic. `Plan(...)` and `Tools(...)` describe the
attempt budget for planner and tool work once execution begins. They do not
configure workflow-engine mechanics such as queue-wait timeouts or heartbeat
liveness. Those deployment concerns belong in the selected engine adapter (for
example `temporal.Options.ActivityDefaults` for the Temporal engine).
`Budget(...)` sets the active-time budget for planner and tool work. The
workflow runtime enforces it directly and reserves a separate finalizer window;
it does not set an engine run timeout. An external-input request ends that
workflow, and the time before a later continuation workflow starts does not
consume the active-time budget.

---

## MCP Server Definition

Enable MCP protocol for a service with `MCP`:

```go
Service("calculator", func() {
    MCP("calc", "1.0.0")

    JSONRPC(func() {
        POST("/mcp")
    })

    Method("add", func() {
        Payload(func() {
            Attribute("a", Int)
            Attribute("b", Int)
        })
        Result(func() {
            Attribute("sum", Int)
        })
        Tool("add", "Add two numbers")  // Mark as MCP tool
    })

    Method("readme", func() {
        Result(String)
        Resource("readme", "file:///docs/README.md", "text/markdown")
    })

    StaticPrompt("greeting", "Friendly greeting",
        "assistant", "You are a helpful assistant",
        "user", "Hello!")
})
```

Tool, resource, prompt and completion bindings select their generated MCP
operations. `SubscriptionSource()` selects one catalog, resource and task change stream;
it does not create a model tool or executor. Other methods in the same
service retain their ordinary HTTP or gRPC contract. For example, an HTTP-only
`health` method can serve `/health` without becoming a model-callable tool.

Construct the MCP adapter with the application's configured Goa endpoints:

```go
endpoints := genservice.NewEndpoints(service, interceptors)
endpoints.Use(middleware)
adapter := genmcp.NewMCPAdapter(endpoints, nil)
```

Omit the interceptor argument when the design declares none. The adapter calls
these endpoints for tools, resource reads, method-backed prompts, completion and
authored resource subscription streams.

The generated MCP HTTP server accepts allowed origins as the final variadic
string arguments to `New`. Pass the exact browser origins you allow, or omit them
to reject every request that sends an Origin header. Configure HTTP middleware
with `Server.Use`, then use `Mount(mux)` or serve the server directly. Both paths check MCP headers, metadata,
HTTP methods and origins before running configured middleware. Origin settings
belong to construction; `MountWithOrigins` and the public inner `Handler` field
are removed in this breaking upgrade. Use `ServeHTTP` for direct serving.
Regenerate the server and update its constructor callers together.
When a generator plugin declares a required server dependency through Goa's
construction plan, pass that typed value before the final origin arguments.
Native example startup calls the matching application factory. Fill in that
factory's application configuration before starting the example server.
For routes with URL parameters, register `ServeHTTP` with the same mux passed
to `New`, or use `Mount(mux)`. The mux supplies path values to generated decoders.

The original endpoint owns authentication, method scopes, the authenticated
context, interceptors and middleware. Static catalogs and prompts do not call
an application endpoint. This replaces the constructor that accepted a bare
service; regenerate and update application wiring together.

The generated HTTP server's `Use` method applies HTTP middleware to direct and
mounted MCP requests. It may be called before or after mounting, but before
requests begin.
Protocol checks run first; the middleware then receives the valid request and
may change its context or return its own response before service dispatch.
Regenerate the server with the pinned Goa dependency to enable this behavior.

### URL values and mapped attributes

Use Goa's native `Param("payload_field:url_name")` notation when a URL wildcard
has a different name from its service payload field:

```go
JSONRPC(func() {
    POST("/organizations/{organization}/mcp")
    Param("organization_id:organization")
})
```

API and parent service prefixes retain their authored paths and mappings. The
URL supplies `organization_id`; tool and prompt arguments, examples, field
metadata and argument codecs exclude it. An independent domain field named
`organization` remains an argument. Each selected method keeps its own type,
custom Go field name and validation, even when several tools share that URL.
Invalid URL values stop before the configured endpoint runs. URL fields use
scalar values or arrays of scalar values, following Goa's HTTP contract.
Unsupported object collections fail generation with the method and field named.

Generated protocol clients carry URL values in their typed request payload,
separate from JSON-RPC parameters. A generated `NewCaller` accepts the URL values
in route order after its retry policy and fixes them for that caller's lifetime.
For this route, pass `"blue"` as its final argument. Tool calls then supply only
domain arguments. The imported `NewHTTPCaller` instead receives a complete
endpoint URL, such as `https://example.com/organizations/blue/mcp`.
Regenerate clients, servers and agent contracts together when upgrading.

### Secured MCP methods

Declare credentials with Goa's security DSL, such as `Token`, `AccessToken`,
`BearerToken`, `APIKey`, `Username` and `Password`. The generator excludes those
annotated fields from tool schemas, examples, field metadata and argument codecs.
It preserves ordinary domain fields, including a field named `token` that has no
security annotation. A credential-only tool accepts omitted arguments or `{}`;
a fixed resource may also use a payload containing only credentials.

The generated HTTP binding supplies credentials separately from JSON-RPC
parameters. It uses the method's authored JSON-RPC binding when present,
otherwise its HTTP binding, or Goa's implicit `Authorization` header. The adapter
fills the original typed service payload, validates it with Goa's generated
validator and calls the same configured endpoint. Authentication callbacks,
required method scopes and returned context retain their Goa behavior. This
applies to tools, resource reads, method-backed prompts and completions.

OAuth access tokens must use `Authorization: Bearer`. Basic and Bearer may be
separate authentication alternatives when every inactive credential field is
optional. They cannot both be required in one request because they share one
`Authorization` header. Generation rejects body credentials, conflicting native
bindings, and credentials bound to MCP's protocol headers instead of silently
choosing another location. Goa also rejects defaults on security fields.

Declare the resource's basic access policy with native Goa `Security` inside an
MCP block:

```go
MCP("records", "1", func() {
    Security(resourceOAuth, func() { Scope("catalog:read") })
})
```

Each resource policy alternative uses one OAuth2, JWT or Bearer scheme, and all
alternatives use the same authored scheme. Without an explicit MCP block policy,
the generator uses a service-level bearer policy or the API policy. Unsupported
inherited API policies fail generation instead of producing public catalogs.
Basic and API-key method authentication retain their original behavior when no
resource policy is selected.

The generated server requires a `*mcp.ResourceServer` constructed by the host.
It verifies resource access before HTTP middleware, including for catalogs,
notifications and unsupported HTTP methods. Operation scopes from the same
authored resource scheme combine with basic-access scopes; alternatives remain
alternatives. Independent domain keys keep their separate native bindings.
An independent credential cannot share the resource's `Authorization` header.
Static catalogs never invoke a domain method as an authentication probe.

Regenerate protected servers and supply the new required constructor dependency.
Original endpoint security remains intact. See [runtime construction and
identity](runtime.md#mcp-resource-servers). Complete OAuth and extension support
remain required before this breaking upgrade is released.

### Goa result views

A method with one Goa result view, or an explicit `Result(type, func() {
View("name") })`, returns exactly that view's fields through MCP. The tool's
advertised output schema and generated agent result codec use those same fields
and their required-field rules. A required field outside the selected view is
omitted; it is never reconstructed as a zero value. Nested fields may select
different views of the same result type, and each keeps its own field contract.

Prompt, resource and completion views must retain the fields required by their
MCP operation. Generation rejects a view that omits those fields. Regenerate
servers and agent toolsets together when changing a view.

When the service chooses among multiple views during execution, a tool or JSON
resource returns Goa's tagged `OneOf` shape. For example, the `default` view
returns `{"type":"default","value":{"visible":"shown"}}`, while a `detailed`
view can retain additional fields inside `value`. The original endpoint supplies
the view name; callers do not add a framework view argument. The advertised
schema, generated agent decoder and stored result retain that name, even when
two views contain identical fields. Each branch enforces its own required fields
and rejects omitted fields. An empty viewed collection returns
`{"type":"default","value":[]}`.

Method-backed prompts, resource-template reads and completion still return their
flat MCP protocol shape. Every selectable view must retain the fields required
by that operation, and only the selected view is validated before conversion.
Changing a fixed result to execution-selected views changes the result wire
shape; regenerate servers and consumers and deploy them together.

### MCP progress from unary methods

Progress does not change a method's payload, result, or unary service interface.
Its implementation calls `mcp.ReportProgress` through the ordinary request
context and handles delivery errors. The generated transport owns the client
token, ordered notifications and final response. HTTP and stdio consumers use
`mcp.WithProgress`; agent activities forward typed updates to the selected host
stream. See [request-scoped progress](runtime.md#request-scoped-mcp-progress)
for callback, cancellation and retry behavior.

### MCP resource content

Resource methods have no payload and return the content for their declared URI.
A `Bytes` result becomes base64 in the protocol's `blob` field, with the MIME
type declared by `Resource`. Named byte types use the same representation.
A string with a `text/` MIME type becomes `text` unchanged. Other result types
with an `application/json` MIME type become `text` encoded by the generated
result codec.

```go
Method("logo", func() {
    Description("Read the service logo")
    Result(Bytes)
    Resource("logo", "asset://logo", "image/png")
})
```

Empty byte results produce a present empty `blob`; empty strings produce a
present empty `text`. The generated direct client rejects resource replies that
contain both fields or neither field. The service returns ordinary typed data;
it does not encode base64 itself or construct protocol content.

### Parameterized MCP resources

`ResourceTemplate(name, uriTemplate, mimeType)` advertises an RFC 6570 address
on an ordinary unary Goa method. `ResourceReader()` selects that same reader
without requiring a template declaration. All templates in one MCP service use
the same reader method. Its payload contains only a required `uri: String`;
the generated constructor preserves the client's exact URI, including percent encoding.

Templates describe addresses clients can construct. They do not grant access,
choose between competing handlers, or recover original variable values. For
example, `{id:3}` may turn `abcdef` into `abc`, so the reader receives the URI
containing `abc`. The reader owns interpretation, existence and current access.
It may serve a valid URI that is not enumerated in a catalog. Fixed `Resource`
bindings retain their exact dispatch before the parameterized reader.

The result declares `contents: ArrayOfRequired(Item)`. Each item declares only
one required `content` OneOf with `text` and/or `blob` object branches. Text
requires `uri: String` with `Format(FormatURI)` and `text: String`; blob requires
that URI and declares `blob: Bytes`. Both allow optional `mimeType` and raw
`_meta` as in embedded prompt resources. The generator validates the typed result
and copies every item in service order, converting bytes to base64. Renamed fields,
named types and located declarations retain their Goa representation.

Make `contents` optional when an existing resource can contain no items. Required
contents must declare `MinLength(1)`. An empty successful resource returns the
present wire array `[]`; an unknown resource must return `invalid_params`, never
an empty success. Nil results, null items, unset variants and invalid content
return `internal_error`. Ordinary Goa composition and the service own access
checks; the framework never opens a filesystem path or fetches a supplied URI.

Resource-capable services also expose `resources/templates/list`, returning
an empty array when no templates are declared. Without a dynamic catalog
binding, the generated fixed catalog has private zero-duration cache metadata
and rejects cursors because it fits in one response.

`ResourceCompletion(uriTemplate, variable)` binds a declared template variable
to the same typed partial-value/prior-arguments/suggestions method contract as
[PromptCompletion](#mcp-prompt-argument-suggestions). The client references the
exact declared template. Variable names are derived during generation, including
prefix and composite variables. Unknown templates, variables and prior names
fail before dispatch; a declared variable without a provider returns `[]`.
Completion does not expand a URI or read the resource.

### Native job tools

`TaskExchange(read, answer, cancel)` binds a creator to three ordinary methods
in the same Goa service. Use it when the application already owns durable jobs:

```go
Method("create_report", func() {
    Description("Accept a durable report job and return its readable state")
    Payload(CreateReportPayload)
    Result(ReportObservation)
    TaskExchange("read_report", "answer_report", "cancel_report")
    Tool("create_report", "Create a report")
})
```

`ReportObservation` is a named type with required `task` metadata and a required
`outcome` OneOf. Declare all five branches: empty `working` and `cancelled`
objects, `input_required` questions, the domain result in `complete`, and a
JSON-RPC error in `failed`. Metadata requires string `taskId`, `createdAt` and
`lastUpdatedAt` fields. Optional `statusMessage` is a string; optional `ttlMs` and
`pollIntervalMs` are `Int64` milliseconds. Absent native `ttlMs` means unlimited
retention; generated MCP replies always emit `ttlMs`, including explicit null.
The service owns the meaning and lifetime of each observation's values.

The read method returns that same named observation. Read and cancel require a
string `taskId`. Answer requires `taskId` and a typed `responses` map. The
`input_required` branch contains a required typed `requests` map. Their values
contain matching `request` and `response` OneOf declarations, one branch per
question kind. Form and URL question shapes follow
[additional input](#additional-input-from-mcp-methods). Keys identify questions
within a job and cannot be reused after an answer. Answer and cancel return no
domain result. `failed` requires integer `code` and string `message`; optional
`data` uses `Any` with the existing `rawjson.Message` field-type metadata to
preserve exact JSON error details.

The service must durably create the job and make it readable before returning
its first observation. Each later method authorizes the native job ID. Generated
MCP adapters call the configured endpoints, retaining security, interceptors,
middleware, mapped HTTP fields, aliases and defaults. Later MCP requests supply
only the saved job ID, typed answers, credentials and route fields. Generation
rejects a required domain field those requests cannot supply. Derive such data
from the job inside the service rather than saving creator arguments in an
adapter-owned store.

A declared `InputExchange` can precede creation. After creation, `tasks/get`
reads the existing job, `tasks/update` submits answers, and `tasks/cancel` accepts
cancellation intent. Update and cancel acknowledgments do not prove the job has
already changed. The completed branch alone becomes the advertised tool result.
`ToolContent` and read-selected Goa views use their ordinary output contracts;
job metadata and presentation attachments stay out of structured domain output.
Local `BindTo` executors and generated registry providers use the same typed
question conversion and workflow-owned continuation operations.

Generated servers with a Task binding advertise the Tasks extension. A client
must declare support on each creating tool call; otherwise the adapter returns
`-32021` before work starts. Ordinary tools retain ordinary replies. Applications
must regenerate affected executors, providers, servers and clients together;
there is no compatibility alias or older Task wire representation. See
[Task client and workflow behavior](runtime.md#mcp-task-clients).

### Authenticated tool and prompt catalogs

`ToolCatalog()` and `PromptCatalog()` bind unary Goa methods to paginated
`tools/list` and `prompts/list`. The method selects the declared names visible
to this request and owns cursor validity, ordering and authorization. Generated
code supplies each selected operation's existing schema and metadata.

```go
Method("list_tools", func() {
    Payload(func() {
        Attribute("cursor", String, "Opaque cursor from the preceding page")
    })
    Result(func() {
        Attribute("tools", ArrayOf(String), "Visible names declared with Tool")
        Attribute("nextCursor", String, "Opaque cursor for another page")
    })
    ToolCatalog()
})
```

For `PromptCatalog`, return `prompts` instead of `tools`. Names select authored
static or method-backed prompts. Both page inputs contain only an optional
`cursor` string apart from native credentials and mapped URL values. Both page
results contain the names array and an optional `nextCursor` string. String
aliases, inherited inputs, custom Go selectors and Goa result views retain
normal generated representations. Every selected view must include its names
array. Make the array optional when an empty page is valid.

An unknown or repeated returned name is a server contract error; generated
adapters do not invent definitions or silently drop entries. Catalog membership
is independent of invocation authorization. Each tool or prompt operation still
runs its original configured endpoint. A name absent from one page does not
become an invocation permission rule. Catalogs do not vary with connection state.
Without a catalog binding, the generated fixed list keeps its existing behavior.

The same `SubscriptionSource()` can select optional `toolsListChanged` and
`promptsListChanged` boolean fields when the corresponding catalog is authored.
Its `acknowledged` object contains those same fields. `tools_changed` and
`prompts_changed` each contain an empty object. The service first acknowledges
an authorized subset, then sends changes for accepted catalogs. Absence or
false leaves a catalog unselected. Native aliases, credentials and mapped URL
values follow the same generated constructor and endpoint path as resource and
Task subscriptions. The shared transport rejects unrequested acceptance,
changes before acknowledgment and duplicate acknowledgment, and attaches the
originating request identity. Discovery advertises `listChanged` only for the
catalogs that this source actually declares.

A catalog method may exist without change notifications. Fixed catalogs cannot
advertise a changing source.

### Authenticated resource and template catalogs

`ResourceCatalog()` and `ResourceTemplateCatalog()` bind unary Goa methods to
`resources/list` and `resources/templates/list`. Both require the service's
`ResourceReader`. The catalog method owns authorization, order and opaque cursor
validity. Reading a URI still invokes its resource owner with current credentials.
A listed descriptor grants no read permission, and the reader can serve URIs
that are not listed.

Declare descriptor types before the service:

```go
var ListedResource = Type("ListedResource", func() {
    Field(1, "uri", String, "Exact resource address", func() { Format(FormatURI) })
    Field(2, "name", String, "Resource identifier")
    Field(3, "title", String, "Display name")
    Required("uri", "name")
})

// Inside the MCP service:
Method("list_resources", func() {
    Description("List the resources visible to this caller")
    Payload(func() { Attribute("cursor", String, "Cursor from the previous page") })
    Result(func() {
        Attribute("resources", ArrayOfRequired(ListedResource), "Visible resources")
        Attribute("nextCursor", String, "Cursor for another page")
    })
    ResourceCatalog()
})
```

For templates, return `resourceTemplates: ArrayOfRequired(Template)` and bind
`ResourceTemplateCatalog()`. Each descriptor requires `uriTemplate: String` and
`name: String`. Templates use RFC 6570 syntax; reading receives the expanded URI
and does not infer the original variables. Both catalog payloads contain only
optional `cursor`, apart from native credentials and mapped URL fields. Make the
entries array optional when empty pages are valid. Each result view must retain
the entries and their required descriptor fields.

Both descriptor kinds allow `title`, `description`, `mimeType`, `icons`,
`annotations` and raw JSON `_meta`. Resource descriptors also allow `size`, the
nonnegative number of raw content bytes before base64 encoding. Icons require
`src` with `FormatURI`; optional fields are `mimeType`, `sizes` (a string array)
and `theme` (`light` or `dark`). Annotations allow `audience` (a `user`/`assistant`
string array), `priority` (a number from zero through one, inclusive) and
`lastModified` (a string). Declare these fields with the corresponding Goa
validation. `_meta` uses the same raw JSON object declaration as rich content;
numbers are retained exactly. Unknown fields, invalid metadata or templates,
and duplicate addresses in one page are contract errors rather than silently
removed entries. Generated clients also reject invalid descriptors from peers.

The same `SubscriptionSource()` can select optional `resourcesListChanged`.
Its acknowledgment contains that boolean and its `resources_changed` branch
contains an empty object. This selection covers both resource catalog kinds.
The service acknowledges an authorized subset first, then reports changes.
It does not imply URI update subscriptions: discovery advertises `listChanged`
and `subscribe` independently according to the source's declared selections.

### Resource update subscriptions

`SubscriptionSource()` binds one server-streaming Goa method per MCP service.
The service must already declare a `Resource` or `ResourceReader` (including
a reader selected by `ResourceTemplate`). Its input
selects optional `resources`, an array of URI strings, apart from native
annotated credentials and mapped URL fields. The same source may also select
Tasks as described below. Each URI declares `Format(FormatURI)`. Empty selections
are valid, so the array is optional.

The stream result contains only a required `change` OneOf with two object
branches. `acknowledged` contains only optional `resources` with the same URI
constraints. `updated` contains only a required `uri` string with `FormatURI`.
Declare types before the service, then bind its streaming method:

```go
var SubscriptionURI = Type("SubscriptionURI", String, func() {
    Format(FormatURI)
})
var AcceptedResources = Type("AcceptedResources", func() {
    Attribute("resources", ArrayOf(SubscriptionURI), "Authorized requested URIs")
})
var UpdatedResource = Type("UpdatedResource", func() {
    Attribute("uri", SubscriptionURI, "The changed resource URI")
    Required("uri")
})
var ResourceChange = Type("ResourceChange", func() {
    OneOf("change", "One acknowledgment or resource update", func() {
        Attribute("acknowledged", AcceptedResources, "Accepted URI subset")
        Attribute("updated", UpdatedResource, "Changed resource or sub-resource")
    })
    Required("change")
})

// Add this method to a service with a fixed resource or URI reader.
Method("watch_resources", func() {
    Description("Authorize requested resources and report their changes")
    Payload(func() {
        Attribute("resources", ArrayOf(SubscriptionURI), "Requested resource URIs")
    })
    StreamingResult(ResourceChange)
    SubscriptionSource()
})
```

The implementation sends one `acknowledged` value with an authorized subset of
requested URIs before sending updates. It can acknowledge an empty subset and
return successfully. An update can identify a related sub-resource: the service
owns that relationship and access checks. The transport does not infer access
from URI prefixes, templates or filesystem paths. Check every `Send` or
`SendWithContext` error and honor the method context's cancellation.

Generated code calls the application's configured Goa endpoint, preserving
security scopes, authenticated context, interceptors and middleware. It uses the
generated input constructor and result validator, including renamed fields and
located types. Results with missing fields, unset variants or invalid URIs fail
before transmission. Acknowledgment outside the requested subset, repeated
acknowledgment and updates before acknowledgment fail the source operation.
Returning successfully without an acknowledgment returns `internal_error`.
Normal return supplies the finished result; `Close`, when present on Goa's stream
interface, prevents further authored sends.

The generated HTTP mount owns each listen request's exact ID and event framing.
Applications do not call raw protocol reporting functions or choose IDs. Closing
the HTTP listener cancels its service context; no reconnect or replay occurs.
Only services whose source selects resources advertise `resources.subscribe`. Fixed catalogs
remain fixed, and unsupported catalog-change requests are omitted from the
acknowledgment. See [client subscription behavior](runtime.md#mcp-change-subscriptions).

### Task update subscriptions

Use the same `SubscriptionSource()` when an application reports native job
changes. Its optional `tasks` object contains optional arrays named after the
service's Task creator methods. Each array holds native string job IDs, not MCP
handles. A Task-only source needs no resource declaration. A combined source
selects both `resources` and `tasks` in one method.

```go
var SelectedReports = Type("SelectedReports", func() {
    Attribute("create_report", ArrayOf(String), "Native report job IDs")
})
var AcceptedReports = Type("AcceptedReports", func() {
    Attribute("tasks", SelectedReports, "Authorized requested jobs")
})
var ChangedReports = Type("ChangedReports", func() {
    Attribute("tasks", SelectedReports, "Native jobs whose state changed")
    Required("tasks")
})
var ReportChanges = Type("ReportChanges", func() {
    OneOf("change", "Accepted jobs or changed job IDs", func() {
        Attribute("acknowledged", AcceptedReports, "Authorized job subset")
        Attribute("tasks_updated", ChangedReports, "Changed native jobs")
    })
    Required("change")
})
Method("watch_reports", func() {
    Description("Authorize requested report jobs and report their changes")
    Payload(func() {
        Attribute("tasks", SelectedReports, "Requested native jobs")
    })
    StreamingResult(ReportChanges)
    SubscriptionSource()
})
```

The acknowledgment must contain the same selection fields and creator names as
the input. Send it once with the authorized subset, then send `tasks_updated`
with changed native IDs. A combined source also declares the resource `updated`
branch. Generation rejects unknown creators, ordinary methods, unexposed Task
creators and mismatched selections. An unrequested acknowledgment or a job update
outside the accepted subset fails before any observation read.

For each changed job, generated code calls its existing configured read endpoint
and sends the full snapshot using the same result codec as `tasks/get`. It fills
that endpoint's native credentials and mapped URL fields from the listen request.
The read-selected view and typed content remain intact. Multiple exposed tools
for one native creator share one native selection, while each accepted MCP handle
receives its corresponding snapshot. This mapping lasts only for the listen
request; it stores no jobs or original tool arguments. The transport owns request
IDs, ordering and closure. The source owns authorization and change detection.

### Method-backed MCP prompts

Declare `Prompt(name, description)` on an ordinary unary Goa method. Its payload
contains named strings, and its result contains ordered messages. The client
selects the prompt and supplies its arguments; the service produces the messages.
The adapter does not call a model or change the agent prompt registry.

```go
var ReviewText = Type("ReviewText", func() {
    Attribute("text", String, "Message text")
    Required("text")
})
var ReviewMessage = Type("ReviewMessage", func() {
    Attribute("role", String, "Message author", func() {
        Enum("user", "assistant")
    })
    OneOf("content", "Selected message content", func() {
        Attribute("text", ReviewText, "Text content")
    })
    Required("role", "content")
})

Service("reviews", func() {
    MCP("reviews", "1.0")
    JSONRPC(func() { POST("/reviews") })
    Method("review", func() {
        Payload(func() {
            Attribute("code", String, "Source code to review", func() {
                MinLength(1)
            })
            Required("code")
        })
        Result(func() {
            Attribute("description", String, "Prompt purpose")
            Attribute("messages", ArrayOfRequired(ReviewMessage), "Ordered messages")
        })
        Prompt("review", "Review source code")
    })
})
```

`prompts/list` describes argument names, descriptions and required fields without
calling the service. `prompts/get` applies the payload's Goa defaults and
validation, then calls the method once. Unknown argument names, null values and
non-string values are invalid parameters. Empty strings remain strings; their
validity comes from the authored payload validation.

Messages use `ArrayOfRequired` to reject null entries. Their required role permits
`user`, `assistant`, or a declared subset. Content uses `OneOf` with one or more
of the following branch names. Each branch is an object with these fields:

| Branch | Required fields | Optional fields |
| --- | --- | --- |
| `text` | `text: String` | `annotations`, `_meta` |
| `image`, `audio` | `data: Bytes`, `mimeType: String` | `annotations`, `_meta` |
| `resource_link` | `uri: String`, `name: String` | `title`, `description`, `mimeType`, `size`, `icons`, `annotations`, `_meta` |
| `resource` | `resource: OneOf` | `annotations`, `_meta` |

The embedded `resource` selects a `text` object with required `uri` and `text`,
or a `blob` object with required `uri` and declared `blob: Bytes`. Both permit
`mimeType` and `_meta`. A byte field may be nil or empty; both produce a present
empty base64 string. Other required fields must use Goa `Required`.

URI fields declare `Format(FormatURI)`. Annotation audience values permit only
`user` and `assistant`; priority is a `Float64` between 0 and 1 inclusive for
**each content item**, independent of other items. Resource-link `size` is a
nonnegative `Float64` describing the resource's byte count. Both numbers must
be finite; a service result containing `NaN` or infinity returns an internal
error before response encoding. Icon objects declare
required `src` with URI format and optional `mimeType`, string `sizes`, and
`theme` restricted to `light` or `dark`; object arrays use `ArrayOfRequired`.
Open `_meta` objects use `Any` with `Meta("struct:field:type", "json.RawMessage",
"encoding/json")`, because each extension owns its fields. Other Go field type
replacements are rejected; use ordinary Goa named or located types.

Generation rejects unsupported fields or missing protocol constraints rather
than losing data. The service returns generated Goa union values and raw bytes;
the adapter produces MCP's flat content objects and base64. Invalid service
results return a protocol internal error. Optional `messages` permits an empty
message sequence; requiring a nonempty sequence remains an authored domain rule.
A method may also be a tool when its tool contract is valid. Static prompts keep
their fixed role/text pairs. Methods can request additional user input through
[`InputExchange`](#additional-input-from-mcp-methods).

### Additional input from MCP methods

`InputExchange(continuationField, outcomeField)` lets a unary tool, resource
reader or prompt return a question and finish after the host answers. The method
keeps its ordinary Goa payload and result. Generation derives the form schema,
answer decoding and result conversion from those same types.

```go
var EmptyAnswer = Type("EmptyAnswer", func() {})
var LabelContent = Type("LabelContent", func() {
    Field(1, "label", String, "Label selected by the user")
    Required("label")
})
var LabelAccepted = Type("LabelAccepted", func() {
    Field(1, "content", LabelContent, "Accepted label")
    Required("content")
})
var LabelAnswer = Type("LabelAnswer", func() {
    OneOf("answer", "User decision", func() {
        Attribute("accept", LabelAccepted, "Accepted form")
        Attribute("decline", EmptyAnswer, "Explicit refusal")
        Attribute("cancel", EmptyAnswer, "Dismissed question")
    })
    Required("answer")
})
var LabelContinuation = Type("LabelContinuation", func() {
    Field(1, "state", String, "Opaque state returned by this operation")
    Field(2, "responses", "Answers returned for this input round", func() {
        Field(1, "label", LabelAnswer, "Answer to the label question")
    })
})
var LabelPending = Type("LabelPending", func() {
    Field(1, "state", String, "Opaque state for the next round")
    Field(2, "requests", "Questions selected for this input round", func() {
        Field(1, "label", "Label question selected by the service", func() {
            Field(1, "message", String, "Question shown to the user")
            Required("message")
        })
    })
})
var LabelOutcome = Type("LabelOutcome", func() {
    OneOf("outcome", "Completed label or requested input", func() {
        Attribute("complete", String, "Completed label")
        Attribute("input_required", LabelPending, "Question for the host")
    })
    Required("outcome")
})

// Inside an MCP-enabled service:
Method("choose_label", func() {
    Description("Collects a user-selected label before completing the operation.")
    Payload(func() {
        Field(1, "continuation", LabelContinuation, "Input supplied by the host")
    })
    Result(LabelOutcome)
    InputExchange("continuation", "outcome")
    Tool("choose_label", "Choose a label with user input")
})
```

The continuation object is optional. Its `state` and `responses` fields are
optional too. The result contains only the required outcome union, with
`complete` and `input_required` branches. Pending `requests` and continuation
`responses` declare the same optional question identifiers; the service chooses
which questions to return in each round. Missing answers can produce another
input round. Unknown response identifiers are ignored.

A form question declares a required `message: String`. Its accepted answer
contains only required `content`, a flat object of primitive fields or non-null
string-selection arrays. Goa descriptions, defaults and supported constraints
supply the form schema. Authored JSON tags select the same field names in the
schema and answer decoder. Hidden fields and JSON tag options that change the
value encoding are rejected. Unsupported form constraints fail generation. Equivalent
integer spellings such as `3`, `3.0` and `3e0` decode to the same typed integer;
fractional and out-of-range values fail before endpoint execution.

A URL question declares required `message` and `url: String` with
`Format(FormatURI)`. Its accept, decline and cancel branches are empty objects.
The host must show the URL and obtain consent before opening it. Acceptance
means consent, not proof that the external interaction has finished; the service
checks completion on the next round. Forms must not request secrets or payment
credentials. Sensitive interactions use URL consent.

Each pending reply supplies requests, state, or both. An explicitly empty
requests object and an explicitly empty state string are valid; neither means
absence. The client echoes the state exactly. The service must authenticate the
caller again and verify state integrity and ownership before trusting it.
Original Goa security, method scopes, interceptors and endpoint middleware run
on every round. State and host answers remain outside model arguments, and only
the completed branch enters the tool's advertised result contract. Fixed and
service-selected Goa views still govern completed fields.

The server checks the capabilities on the current request before returning
questions. Unsupported modes return JSON-RPC `-32021` with typed
`requiredCapabilities` data. Invalid mode declarations return invalid params.
The current protocol defines `elicitation: {}` as form-only support; URL support
must be explicit. Generated servers always emit the selected `form` or `url`
mode. See the [current elicitation contract](https://modelcontextprotocol.io/specification/2026-07-28/client/elicitation).

Regenerate typed JSON-RPC consumers: tool, resource-read and prompt-read results
now have native `Outcome` unions. Select `AsComplete()` or `AsInputRequired()`
instead of reading completed fields directly. Framework MCP callers keep their
existing `CallResponse` contract.

The same `InputExchange` method can back an agent tool through `BindTo` or a
generated registry provider. Inherited `Args` exclude the continuation; inherited
`Return` selects the completed branch. `Inject` keeps filling ordinary runtime
fields. The runtime owns host answers and the input-round number, suspends the
unfinished call, and invokes the same method after validating the host response.
Pending questions never enter completed tool history. `BoundedResult` and
`FromMethodResultField` read completed fields. Registry output follows the tool's
`Return`, independently of HTTP result views. Text-only calls reject continuation
before invocation and reject unfinished output before suspension.

Regenerate local executors and registry providers together with their callers.
Custom result mappers receive the completed native branch for an `InputExchange`
method; they never receive its pending outcome. Do not include the continuation
field in explicit `Args`.

### MCP prompt argument suggestions

`PromptCompletion(promptName, argumentName)` marks a separate unary Goa method
that returns suggestions while a client fills one declared prompt argument.
It does not execute that prompt or call a model. The binding must select an
argument declared by a method-backed `Prompt`; duplicate bindings fail evaluation.

The payload declares required `value: String` and optional
`arguments: MapOf(String, String)`. Both fields must exist in the design:
`value` contains the current partial text, and `arguments` retains previously
resolved values from the client. Unknown prompt, argument and context names
return invalid-parameter errors before dispatch. Ordinary Goa types, renamed
fields, defaults and validators remain available; opaque Go field replacements
are rejected.

The result declares `values: ArrayOf(String)` with `MaxLength(100)` or a stricter
bound, and may declare `total: Int64` and `hasMore: Boolean`. Keep `values`
optional when an empty list is a valid domain result. The generated response
always contains an array, including `[]`. The service owns relevance order,
access control and any fuzzy matching; the framework never sorts or truncates
its suggestions. Invalid output returns an internal-error response.

```go
Method("suggest_review_style", func() {
    Description("Suggest review styles while the client fills the review prompt.")
    Payload(func() {
        Attribute("value", String, "Partial review-style text")
        Attribute("arguments", MapOf(String, String), "Previously resolved prompt arguments")
        Required("value")
    })
    Result(func() {
        Attribute("values", ArrayOf(String), "Suggestions in relevance order", func() {
            MaxLength(100)
        })
        Attribute("total", Int64, "Total matches, including values not returned")
        Attribute("hasMore", Boolean, "Whether further matches exist")
    })
    PromptCompletion("review", "style")
})
```

The MCP protocol limits **one response array** to at most 100 values, inclusive.
It does not limit `total` to 100 or share an allowance across subsequent requests.
A valid declared argument without a completion binding returns empty suggestions.
The server advertises `completions` only when it has a binding. Applications
configure authentication, rate limiting and suggestion access through their
normal Goa service and HTTP composition. URI-template variables use
[ResourceCompletion](#parameterized-mcp-resources) with the same typed contract.

### MCP tool behavior hints

Declare standard MCP annotations inside the method's `Tool` block:

```go
Tool("add", "Add two numbers", func() {
    ToolTitle("Add numbers")
    ReadOnlyHint(true)
    DestructiveHint(false)
    IdempotentHint(true)
    OpenWorldHint(false)
})
```

The generated catalog preserves omitted hints and explicit false values.
`ReadOnlyHint` says the tool leaves its environment unchanged. `IdempotentHint`
says repeating identical arguments has no additional effects; its service
implementation must enforce that promise. `DestructiveHint` describes removal
or replacement of existing data. `OpenWorldHint` describes interaction with
external entities. `ToolTitle` supplies a display name.

These declarations apply only to MCP method tools. They do not grant trust or
change agent activity retry policies. The application chooses trust and bounded
HTTP retries when constructing its caller; see [MCP callers](runtime.md#mcp-callers).

Generated tool and prompt clients can receive all five MCP content kinds,
including media, links and embedded resources with annotations, icons and
extension metadata. Their shared `ContentItem` retains absent versus empty
content and rejects malformed selected variants. Method-backed prompts author
these kinds through typed Goa results. Tool methods use `ToolContent` to return
typed attachments beside structured domain fields.

### Authored MCP tool content

Use `ToolContent(field)` inside a method's `Tool` block to select a top-level
result array. Each element is an ordinary Goa object with one required
`content` OneOf. The service returns typed values; generated code validates and
converts each selected branch to MCP's flat content representation.

```go
var ReportText = Type("ReportText", func() {
    Attribute("text", String, "Text shown to the recipient")
    Required("text")
})
var ReportAttachment = Type("ReportAttachment", func() {
    OneOf("content", "Selected report content", func() {
        Attribute("text", ReportText, "Text content")
    })
    Required("content")
})

Service("reports", func() {
    MCP("reports", "1.0")
    JSONRPC(func() { POST("/reports") })
    Method("read", func() {
        Result(func() {
            Attribute("summary", String, "Structured report summary")
            Attribute("attachments", ArrayOfRequired(ReportAttachment), "Ordered content")
            Required("summary")
        })
        Tool("read", "Read a report", func() {
            ToolContent("attachments")
        })
    })
})
```

This result advertises and returns `summary` as structured JSON. `attachments`
becomes MCP `content`, retaining authored order and audience annotations. It is
absent from the structured output schema, examples, field metadata and exact
agent codecs. This separation also applies to user-only content, icons and
extension metadata, which must not enter model requests as ordinary JSON.

The supported union branches and their fields are the same as
[method-backed prompt content](#method-backed-mcp-prompts): `text`, `image`,
`audio`, `resource_link` and `resource`. Image/audio data and embedded resource
blobs use `Bytes`; conversion writes base64 only at the protocol boundary.
Extra fields with no MCP representation fail generation.

An optional array can be empty. A required array must also declare
`MinLength(1)`. `ArrayOfRequired` rejects null elements, and each item must select
exactly one content branch. Goa result views select both domain fields and
attachments. A view omitting the marked field returns no content. Fixed results
with only that field return content without `structuredContent` or an output
schema. Service-selected views retain the view name in the structured result,
including views with no remaining domain fields.

Regenerate servers and agent packages after adding the binding. The service
method's typed result remains its Goa contract; generated MCP codecs use the
separate structured result contract. There is no second result mode, untyped
content field or compatibility decoder.

### MCP Capabilities


| DSL Function                 | MCP Capability                     |
| ---------------------------- | ---------------------------------- |
| `Tool(name, desc)` in Method | `tools/list`, `tools/call`         |
| `Resource(name, uri, mime)`  | `resources/list`, `resources/read` |
| `Prompt(name, desc)` in Method | `prompts/list`, `prompts/get` with typed arguments and messages |
| `StaticPrompt(...)`          | `prompts/list`, `prompts/get`      |
| `ResourceReader()` / `ResourceTemplate(name, template, mime)` in Method | One service-owned URI reader and optional authored templates |
| `ResourceCatalog()` / `ResourceTemplateCatalog()` in Method | Authenticated pages of typed resource descriptors or URI templates |
| `ToolCatalog()` / `PromptCatalog()` in Method | Authenticated pages of declared tools or prompts |
| `SubscriptionSource()` in Method | `subscriptions/listen` for service-owned catalog, resource and Task updates over HTTP |
| `TaskExchange(read, answer, cancel)` in Method | Task creation from `tools/call`, plus `tasks/get`, `tasks/update` and `tasks/cancel` |
| `ResourceCompletion(template, variable)` in Method | `completion/complete` for declared template variables |
| `PromptCompletion(prompt, argument)` in Method | `completion/complete` for declared prompt arguments |


---

## Registry

`Registry` declares a remote registry source for tool discovery. Registries are
centralized catalogs of MCP servers and toolsets that agents can discover and
consume through generated registry clients.

```go
var CorpRegistry = Registry("corp-registry", func() {
    Description("Corporate tool registry")
    URL("https://registry.corp.internal")
    APIVersion("v1")
    Security(CorpAPIKey)
    Timeout("30s")
    Retry(3, "1s")
    SyncInterval("5m")
    CacheTTL("1h")
})
```

### Federation

Federation configures importing toolsets from external registries with filtering:

```go
var AnthropicRegistry = Registry("anthropic", func() {
    Description("Anthropic MCP Registry")
    URL("https://registry.anthropic.com/v1")
    Security(AnthropicOAuth)
    Federation(func() {
        Include("web-search", "code-execution", "data-*")
        Exclude("experimental/*", "deprecated/*")
    })
    SyncInterval("1h")
    CacheTTL("24h")
})
```

### Publishing to Registries

Use `PublishTo` inside an export to configure registry publication:

```go
Agent("data-agent", "Data processing agent", func() {
    Use(LocalTools)
    Export(LocalTools, func() {
        PublishTo(CorpRegistry)
        Tags("data", "etl")
    })
})
```

### Security for Registries

Registry implements Goa's `SecurityHolder` interface, allowing all Goa security DSL functions
to work inside Registry blocks. This includes `APIKeySecurity`, `OAuth2Security`,
`JWTSecurity`, and `BasicAuthSecurity`.

```go
// API Key authentication
var CorpAPIKey = APIKeySecurity("corp_api_key", func() {
    Description("Corporate API key")
})

var CorpRegistry = Registry("corp-registry", func() {
    URL("https://registry.corp.internal")
    Security(CorpAPIKey)
})

// OAuth2 authentication
var AnthropicOAuth = OAuth2Security("anthropic_oauth", func() {
    ClientCredentialsFlow(
        "https://auth.anthropic.com/oauth/token",
        "",
    )
    Scope("registry:read", "Read access to registry")
})

var AnthropicRegistry = Registry("anthropic", func() {
    URL("https://registry.anthropic.com/v1")
    Security(AnthropicOAuth)
})
```

Multiple security schemes can be added by calling `Security()` multiple times.

---

The default suite identifier is derived from `<service>.<agent>.<toolset>`. Use `Suite()` to
override when you need a specific identifier for cross-platform compatibility.

---

## Agent API Types (Re-exported)

The DSL re-exports standardized agent API types for use in Goa service designs:

- `AgentRunPayload`: input for agent run/start/continuation endpoints
- `AgentRunResult`: terminal result for non-streaming endpoints
- `AgentRunChunk`: streaming progress events
- Supporting types: `AgentMessage`, `AgentToolEvent`, `AgentToolError`,
  `AgentToolFailure`, `AgentRecoveryDirective`, etc.

```go
Service("orchestrator", func() {
    Method("run", func() {
        Payload(AgentRunPayload)
        StreamingResult(AgentRunChunk)
        JSONRPC(func() { ServerSentEvents(func() {}) })
    })
    Method("run_sync", func() {
        Payload(AgentRunPayload)
        Result(AgentRunResult)
    })
})
```

These types map to runtime/planner types via generated conversions and should be used only at API
boundaries.

---

## Generated Artifacts

For each service/agent combination, `goa gen` produces:

### Agent Package (`gen/<svc>/agents/<agent>/`)

- `agent.go` — registers workflows/activities/toolsets; exports `const AgentID agent.Ident`
- `workflow.go` — implements the durable run loop
- `activities.go` — thin wrappers calling runtime activities
- `config.go` — runtime options bundle; supplies the planner; executable toolsets are composed separately

### Toolset Owner Packages (`gen/<svc>/toolsets/<toolset>/`)

Generated once per defining toolset (the owner), and imported by all consumers.

- `types.go` — tool-local payload/result/sidecar Go types
- `codecs.go` — canonical JSON codecs for payload/result/sidecar
- `specs.go` — `[]tools.ToolSpec` entries for the toolset
- `transforms.go` — method-backed transforms when `BindTo` is used and shapes are compatible

### Agent Specs (`specs/`)

The agent package contains an aggregated tool catalog used by planners/runtime.

- `specs.go` — aggregated `[]tools.ToolSpec` for all `Use`d toolsets
- `tool_schemas.json` — backend-agnostic JSON catalog (payload/result JSON Schemas)

Tool schemas are also written to:

```text
gen/<service>/agents/<agent>/specs/tool_schemas.json
```

### Agent Tool Exports (`exports/<export>/`)

Generated when an agent exports toolsets (agent-as-tool). Export packages provide:

- Typed tool identifiers (`tools.Ident` constants)
- Alias payload/result types and codecs
- Registration helpers (for providers) and consumer wiring helpers

### MCP Packages

When a service declares MCP (`MCP(...)`), `goa gen` emits JSON-RPC client/server code under
`gen/jsonrpc/mcp_<service>/...`. Canonical tool contracts and an MCP executor
are generated under `gen/<service>/toolsets/<server-name>/`.

MCP services must declare their service-level JSON-RPC `POST` route explicitly.
For migration from the former MCP subscription, notification, dynamic-prompt,
and SSE-caller APIs, see the
[preview upgrade guide](runtime.md#preview-upgrade-guide).

---

## Wiring Example

```go
rt := runtime.New(
    runtimeStore,
    runtime.WithEngine(temporalClient),
    runtime.WithMemoryStore(mongoStore),
)

if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{
    Planner: myPlanner,
}); err != nil {
    log.Fatal(err)
}

// MCP toolset wiring
caller, err := mcp.NewHTTPCaller(mcp.HTTPOptions{
    Endpoint: "https://assistant.example.com/mcp",
    ClientInfo: mcp.ClientInfo{
        Name:    "my-agent",
        Version: "1.0.0",
    },
})
if err != nil {
    log.Fatal(err)
}
if err := chat.RegisterUsedToolsets(ctx, rt, chat.WithAssistantExecutor(genassistantmcp.NewMCPExecutor(caller))); err != nil {
    log.Fatal(err)
}

// Execute agent
client := chat.NewClient(rt)
out, err := client.Run(ctx, "session-1", messages)
```

---

## Best Practices

**Keep tool descriptions concise** — Generated helper prompts reuse this text. Write clear,
actionable descriptions that help LLMs choose the right tool.

**Reuse toolsets** — `var Shared = Toolset("shared", ...)` avoids duplication across agents.

**Use specific validations** — `Required`, `MinLength`, `Enum`, `Pattern` give planners strong
schemas with clear constraints.

**Treat run policies as API contracts** — Choose bounds that match downstream SLAs. Document
expected time budgets and tool limits.

**Use `BoundedResult()` for large views** — Mark tools that return potentially large lists,
graphs, or windows as bounded. Services own trimming; the runtime propagates bounds metadata.

**Compose executable toolsets explicitly** — Use generated MCP executors and executor
options to register each shared runtime binding once. See [MCP callers](runtime.md#mcp-callers).

**Use display hint templates** — `CallHintTemplate` and `ResultHintTemplate` improve UI feedback
during tool execution.

**Use Artifact for full-fidelity data** — Keep model payloads bounded; attach rich artifacts
via `Artifact` for downstream consumers.

---

## Transforms and Compatibility (BindTo)

When a tool is bound to a Goa method via `BindTo`, codegen analyzes shapes and emits transform
helpers if compatible:

```go
var SearchPayload = Type("SearchPayload", func() {
    Attribute("query", String); Required("query")
})
var SearchResult = Type("SearchResult", func() {
    Attribute("documents", ArrayOf(String))
})

Service("svc", func() {
    Method("Search", func() {
        Payload(SearchPayload)
        Result(SearchResult)
    })

    Agent("a", "", func() {
        Use("ts", func() {
            Tool("search", "", func() {
                Args(SearchPayload)
                Return(SearchResult)
                BindTo("svc", "Search")
            })
        })
    })
})
```

Generated transforms in `specs/ts/transforms.go`:

```go
// In your executor stub:
args, err := tspecs.UnmarshalSearchPayload(call.Payload)
if err != nil {
    return nil, err
}
mp := tspecs.InitSearchMethodPayload(args)
result := yourClient.Search(ctx, mp)
tr := tspecs.InitSearchToolResult(result)
return planner.ToolResult{Result: tr}, nil
```

Notes:

- Compatibility uses Goa's type system (names and structure, including `Extend`)
- For nested shapes, keep pointers in user types for validators/codecs
- Mapping lives in executors; transforms are conveniences when types align

## Optional presentation

`RequiresUI()` excludes a tool from text-only runs. `UIOnly("renderUi")` hides an
optional Boolean argument and disables it during execution. `UIInstructions(...)`
adds rendering guidance only to the ordinary tool contract.
`ResultReminder(...)` supplies result guidance in both contracts; use
`UIResultReminder(...)` for guidance that assumes the user sees interactive
output. Ordinary contracts combine both reminders with a separating newline,
while text-only contracts retain only `ResultReminder`. Omitted UI controls
decode to an explicit false value, including optional pointer fields. See
[text-only execution](runtime.md#text-only-execution) for the runtime contract.
