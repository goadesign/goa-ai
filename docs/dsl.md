# Goa Agent DSL Reference

This document explains how to author agents, toolsets, and runtime policies with the Goa‑AI DSL.
Use it alongside `docs/overview.md` and `docs/runtime.md` for the broader architecture and runtime
details.

## Overview

- **Import path:** `"goa.design/goa-ai/dsl"` (typically dot-imported alongside Goa's DSL).
- **Entry point:** Declare agents inside a regular Goa `Service` definition. The DSL augments Goa's
  design tree and is processed during `goa gen`.
- **Outcome:** `goa gen` produces agent packages (`gen/<service>/agents/<agent>`), tool
  codecs/specs, activity handlers, and registration helpers. A contextual `AGENTS_QUICKSTART.md` is
  written at the module root unless disabled via `DisableAgentDocs()`.

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
				MaxConsecutiveFailedToolCalls(3),
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

### Agent‑as‑Tool Composition (Child Workflows)

When agent A "uses" a toolset exported by agent B, Goa‑AI wires composition automatically:

- The exporter (agent B) package includes a generated `agenttools` package with typed tool IDs and
  `NewRegistration(rt, systemPrompt, ...runtime.AgentToolOption)` helpers.
- The consumer registers the returned `runtime.ToolsetRegistration` with its runtime. The consumer
  does not need the exporter’s planner locally; it only needs routing metadata.
- At runtime, invoking an exported tool starts the exporter agent as a **child workflow** using the
  generated route metadata. The parent emits `AgentRunStarted` and the returned `ToolResult`
  includes a `RunLink` handle to the child run.

Each run has its own event stream. Stream profiles select which event kinds are emitted to
different audiences and link child runs via run handles rather than flattening run identity.

---

## Function Reference

### Agent Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `Agent(name, description, dsl)` | Inside `Service` | Declares an LLM agent with tool usage/exports and run policy |
| `Use(value, dsl?)` | Inside `Agent` | Declares toolset consumption (referencing or inline definition) |
| `Export(value, dsl?)` | Inside `Agent` or `Service` | Declares toolsets exposed to other agents |
| `AgentToolset(svc, agent, ts)` | Top-level or inside `Use` | References a toolset exported by another agent |
| `UseAgentToolset(svc, agent, ts)` | Inside `Agent` | Combines `AgentToolset` with `Use` |
| `DisableAgentDocs()` | Inside `API` | Disables `AGENTS_QUICKSTART.md` generation |
| `Passthrough(tool, target...)` | Inside exported `Tool` | Forwards tool execution to a Goa service method |

### Toolset Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `Toolset(args...)` | Top-level | Defines a provider-owned toolset |
| `FromMCP(service, toolset)` | Argument to `Toolset` | Configures MCP server as toolset provider |
| `FromRegistry(registry, toolset)` | Argument to `Toolset` | Configures registry as toolset provider |
| `Tags(values...)` | Inside `Toolset` or `Tool` | Attaches metadata labels for categorization |

### Tool Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `Tool(name, description?, dsl?)` | Inside `Toolset` or `Method` | Declares a callable tool |
| `Args(type)` | Inside `Tool` | Defines input parameter schema |
| `Return(type)` | Inside `Tool` | Defines output result schema |
| `Artifact(kind, type)` | Inside `Tool` | Defines typed artifact data (not sent to model) |
| `BindTo(method)` or `BindTo(service, method)` | Inside `Tool` | Binds tool to a Goa service method |
| `Inject(fields...)` | Inside `Tool` | Marks fields as server-injected (hidden from LLM) |
| `CallHintTemplate(tmpl)` | Inside `Tool` | Go template for call display hint |
| `ResultHintTemplate(tmpl)` | Inside `Tool` | Go template for result display hint |
| `BoundedResult(dsl?)` | Inside `Tool` | Marks result as bounded view over larger data; optional sub-DSL can declare paging cursor fields |
| `Cursor(name)` | Inside `BoundedResult(func() { ... })` | Declares which payload field carries the paging cursor (optional) |
| `NextCursor(name)` | Inside `BoundedResult(func() { ... })` | Declares which result field carries the next-page cursor (optional) |
| `ResultReminder(text)` | Inside `Tool` | Static system reminder injected after tool result |
| `Confirmation(dsl)` | Inside `Tool` | Declares that tool execution must be explicitly approved out-of-band |

### Tool payload defaults (Feature)

Tool payload defaults follow Goa’s request‑style semantics:

- Codecs decode JSON into helper “decode‑body” structs with pointer fields to distinguish **missing**
  from **zero**.
- Codecs then transform helper → final payload using Goa’s transform generator, which injects
  default values deterministically.
- As a result, optional primitive fields with defaults are emitted as **value fields** (non‑pointers)
  in the final tool payload type.

See [`docs/tool_payload_defaults.md`](tool_payload_defaults.md) for the complete contract and the
generator invariants.

### Bounded results (returned / total / truncated / refinement_hint)

`BoundedResult` exists so tools can return a bounded view (caps, window clamping, downsampling)
while the runtime can reliably detect truncation and guide the planner.

Canonical bounds fields:

- `returned` (**required**, Int)
- `truncated` (**required**, Boolean)
- `total` (optional, Int)
- `refinement_hint` (optional, String)

Authoring rule (no hybrids):

- Either declare **none** of these fields and let `BoundedResult()` add all of them, or
- Declare **all** of them with the correct types/requiredness.

### Pagination (cursor / next_cursor)

Tools that return potentially large datasets should support cursor-based pagination when practical.
Cursor-paged tools identify the two paging fields in their own payload/result schemas:

- **payload cursor field** (declared via `Cursor("field_name")`)
- **result next-cursor field** (declared via `NextCursor("field_name")`)

Contract:

- Treat cursors as **opaque**: do not parse, modify, or synthesize them.
- When paging, keep **all other arguments unchanged**; only set the payload cursor field.
- Paged tools should also be `BoundedResult(...)` tools and surface truncation metadata
  (returned/total/truncated/refinement hints) alongside results.

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
        PromptTemplate(`Approve write: set {{ .Key }} to {{ .Value }}`)
        DeniedResultTemplate(`{"summary":"Cancelled","key":"{{ .Key }}"}`)
    })
})
```

Notes:

- The runtime owns how confirmation is requested. The built-in confirmation protocol uses a dedicated
  `AwaitConfirmation` await and a `ProvideConfirmation` decision call. See `docs/runtime.md` for the
  expected payloads and flow.
- Confirmation templates (`PromptTemplate` and `DeniedResultTemplate`) are Go `text/template` strings
  executed with `missingkey=error`. In addition to the standard template functions (e.g. `printf`),
  Goa-AI provides:
  - `json v` → JSON encodes `v` (useful for optional pointer fields or embedding structured values).
  - `quote s` → returns a Go-escaped quoted string (like `fmt.Sprintf("%q", s)`).
- Design-time confirmation is the **common case** (“this tool always needs approval”), but runtimes
  can also require confirmation dynamically for additional tools via `runtime.WithToolConfirmation(...)`
  (see runtime docs).

### Policy Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `RunPolicy(dsl)` | Inside `Agent` | Configures runtime execution constraints |
| `DefaultCaps(opts...)` | Inside `RunPolicy` | Sets resource limits using option functions |
| `MaxToolCalls(n)` | Argument to `DefaultCaps` | Maximum total tool invocations |
| `MaxConsecutiveFailedToolCalls(n)` | Argument to `DefaultCaps` | Maximum consecutive failures before stopping |
| `TimeBudget(duration)` | Inside `RunPolicy` | Maximum wall-clock execution time (e.g., "5m") |
| `InterruptsAllowed(bool)` | Inside `RunPolicy` | Enables user interruption handling |
| `OnMissingFields(action)` | Inside `RunPolicy` | Validation behavior: `""`, `"finalize"`, `"await_clarification"`, `"resume"` |

### Timing Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `Timing(dsl)` | Inside `RunPolicy` | Groups timing configuration |
| `Budget(duration)` | Inside `RunPolicy` or `Timing` | Total wall-clock budget for the run |
| `Plan(duration)` | Inside `RunPolicy` or `Timing` | Timeout for Plan and Resume activities |
| `Tools(duration)` | Inside `RunPolicy` or `Timing` | Default timeout for tool activities |

### History Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `History(dsl)` | Inside `RunPolicy` | Configures conversation history management |
| `KeepRecentTurns(n)` | Inside `History` | Retain only the most recent N turns |
| `Compress(triggerAt, keepRecent)` | Inside `History` | Summarize older turns when threshold reached |

### Cache Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `Cache(dsl)` | Inside `RunPolicy` | Configures prompt caching hints |
| `AfterSystem()` | Inside `Cache` | Place cache checkpoint after system messages |
| `AfterTools()` | Inside `Cache` | Place cache checkpoint after tool definitions |

### MCP Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `MCP(name, version, opts...)` | Inside `Service` | Enables MCP protocol for the service |
| `ProtocolVersion(version)` | Option for `MCP` | Sets MCP protocol version (e.g., "2025-06-18") |
| `Tool(name, description)` | Inside `Method` (with MCP enabled) | Marks method as MCP tool |
| `Resource(name, uri, mime)` | Inside `Method` | Marks method as MCP resource provider |
| `WatchableResource(name, uri, mime)` | Inside `Method` | MCP resource with subscription support |
| `StaticPrompt(name, desc, msgs...)` | Inside `Service` (with MCP) | Defines static MCP prompt template |
| `DynamicPrompt(name, description)` | Inside `Method` | Marks method as dynamic prompt generator |
| `Notification(name, description)` | Inside `Method` | Marks method as MCP notification sender |
| `Subscription(resourceName)` | Inside `Method` | Defines subscription handler for a resource |
| `SubscriptionMonitor(name)` | Inside `Method` | Defines SSE monitor for subscriptions |

### Registry Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `Registry(name, dsl?)` | Top-level | Declares a remote registry source |
| `URL(url)` | Inside `Registry` | Sets the registry endpoint URL (required) |
| `APIVersion(version)` | Inside `Registry` | Sets registry API version (default: "v1") |
| `Security(scheme)` | Inside `Registry` | References Goa security scheme for auth |
| `Timeout(duration)` | Inside `Registry` | Sets HTTP request timeout |
| `Retry(maxRetries, backoff)` | Inside `Registry` | Configures retry policy |
| `SyncInterval(duration)` | Inside `Registry` | Sets catalog refresh interval |
| `CacheTTL(duration)` | Inside `Registry` | Sets local cache duration |
| `Federation(dsl)` | Inside `Registry` | Configures external registry imports |
| `Include(patterns...)` | Inside `Federation` | Glob patterns for namespaces to import |
| `Exclude(patterns...)` | Inside `Federation` | Glob patterns for namespaces to skip |
| `PublishTo(registry)` | Inside `Toolset` (in `Export`) | Configures registry publication |
| `Version(version)` | Inside `Toolset` (with `FromRegistry`) | Pins toolset version |

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

- A `*ToolsetExpr` returned by `Toolset` or `FromMCP` (provider-owned)
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

`FromMCP` configures a toolset to be backed by an MCP server. Two patterns are supported:

**Pattern 1: Goa-backed MCP server (same design)**

When your MCP server is defined in the same design using the `MCP` DSL:

```go
Service("assistant", func() {
    MCP("assistant-mcp", "1.0.0")
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

**Pattern 2: External MCP server (inline schemas)**

For external MCP servers, define the schemas inline:

```go
var RemoteSearch = Toolset("remote-search", FromMCP("remote", "search"), func() {
    Tool("web_search", "Search the web", func() {
        Args(func() { Attribute("query", String) })
        Return(func() { Attribute("results", ArrayOf(String)) })
    })
})

Agent("helper", "Helper agent", func() {
    Use(RemoteSearch)
})
```

At runtime, supply an `mcpruntime.Caller` for the toolset ID.

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

`Args` defines the input parameter schema for a tool. It follows the same patterns as Goa's
`Payload` function:

```go
// Inline schema
Args(func() {
    Attribute("query", String, "Search query")
    Attribute("limit", Int, "Max results")
    Required("query")
})

// Reuse existing type
Args(SearchParams)

// Primitive type for simple tools
Args(String)  // Single string parameter
```

`Return` defines the output result schema, following the same patterns as Goa's `Result`:

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

### Artifact (Non-Model Data)

`Artifact` defines structured data attached to tool results that is **not** sent to the model
provider. Use it for full-fidelity data that backs a bounded model-facing result:

```go
Tool("get_time_series", "Get time series data", func() {
    Args(GetTimeSeriesArgs)
    Return(GetTimeSeriesReturn)           // Model sees this (summary/bounded view)
    Artifact("time_series", TimeSeriesData) // Full data for UI/downstream only
})
```

Artifacts are attached to `planner.ToolResult.Artifacts` and accessible via stream events,
but never included in prompts to the LLM.

#### Controlling artifact emission (`artifacts` + `ArtifactsDefault`)

Tools that declare an `Artifact` automatically accept a reserved payload field named `artifacts`
with values `"auto"`, `"on"`, and `"off"`:

- `"on"`: request UI artifacts when available.
- `"off"`: suppress UI artifacts for this call.
- `"auto"` (or omitted): use the tool’s default behavior.

Use `ArtifactsDefault("off")` inside the tool DSL to make artifacts opt-in by default when `"auto"`
or omission is used. This is useful for tools whose artifacts are only appropriate when the user
explicitly asked for a visualization.

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

- `ToMethodPayload_<Tool>(in <ToolArgs>) (<MethodPayload>, error)`
- `ToToolReturn_<Tool>(in <MethodResult>) (<ToolReturn>, error)`

### Inject (Server-Side Fields)

`Inject` marks payload fields as server-injected. Injected fields are:

1. Hidden from the LLM (excluded from JSON schema)
2. Exposed in generated structs with setter methods
3. Populated by runtime hooks (ToolInterceptor)

```go
Tool("get_data", "Get user data", func() {
    Args(func() {
        Attribute("user_id", String, "User to look up")
        Attribute("session_id", String, "Current session")
        Required("user_id", "session_id")
    })
    BindTo("data_service", "get_user")
    Inject("session_id")  // Hidden from LLM, set by runtime
})
```

### Display Hint Templates

`CallHintTemplate` and `ResultHintTemplate` configure Go templates for UI display:

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
    ResultHintTemplate("Found {{ .Count }} results")
})
```

Templates are compiled with `missingkey=error`. Keep hints concise (≤140 characters recommended).
Template variables use Go field names (e.g., `.Query`, `.Limit`), not JSON keys.

### BoundedResult

`BoundedResult` marks a tool's result as a bounded view over potentially larger data. When set:

- Generated `tools.ToolSpec` has `BoundedResult: true`
- Generated `ResultBounds()` method implements `agent.BoundedResult`
- Runtime attaches bounds metadata to hook events and streams

```go
Tool("list_devices", "List devices in scope", func() {
    Args(ListDevicesArgs)
    Return(ListDevicesReturn)
    BoundedResult()  // Result may be truncated
})
```

Services are responsible for trimming and setting the canonical bounds fields on the tool result
(`returned`, `total`, `truncated`, `refinement_hint`).

### Tags

`Tags` attaches metadata labels to tools or toolsets for categorization and filtering:

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

### ResultReminder

`ResultReminder` configures a static system reminder that is injected into the conversation
after the tool result is returned. Use this to provide backstage guidance to the model about
how to interpret or present the result to the user.

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
    ResultReminder("The user sees a rendered graph of this data in the UI.")
})
```

---

## RunPolicy, Caps & History

`RunPolicy` configures execution limits and behavior enforced at runtime:

```go
RunPolicy(func() {
    // Resource limits
    DefaultCaps(
        MaxToolCalls(20),
        MaxConsecutiveFailedToolCalls(3),
    )
    
    // Timing
    TimeBudget("5m")
    Timing(func() {
        Budget("10m")   // Total wall-clock
        Plan("45s")     // Planner activity timeout
        Tools("2m")     // Default tool timeout
    })
    
    // Behavior
    InterruptsAllowed(true)
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

| Option | Purpose |
|--------|---------|
| `MaxToolCalls(n)` | Maximum total tool invocations per run |
| `MaxConsecutiveFailedToolCalls(n)` | Stop after N consecutive failures |

### OnMissingFields Values

| Value | Behavior |
|-------|----------|
| `""` (empty) | Let the planner decide based on context |
| `"finalize"` | Stop execution when required fields are missing |
| `"await_clarification"` | Pause and wait for user input |
| `"resume"` | Continue execution despite missing fields |

### History Policies

History policies transform message history before each planner invocation while preserving
system prompts and logical turn boundaries.

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
        Compress(30, 10)  // Trigger at 30 turns, keep 10 recent
    })
})
```

When `Compress` is used, the generated agent config includes a `HistoryModel` field that
callers must supply with a `model.Client`. The runtime uses `ModelClassSmall` for
summarization.

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
        Budget("10m")  // Total wall-clock for the run
        Plan("45s")    // Timeout for Plan/Resume activities
        Tools("2m")    // Default timeout for tool activities
    })
})
```

---

## MCP Server Definition

Enable MCP protocol for a service with `MCP`:

```go
Service("calculator", func() {
    MCP("calc", "1.0.0", ProtocolVersion("2025-06-18"))

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

    Method("status", func() {
        Result(func() {
            Attribute("status", String)
        })
        WatchableResource("status", "status://system", "application/json")
    })

    StaticPrompt("greeting", "Friendly greeting",
        "system", "You are a helpful assistant",
        "user", "Hello!")
    
    Method("code_review", func() {
        Payload(func() {
            Attribute("language", String)
            Attribute("code", String)
        })
        Result(ArrayOf(Message))
        DynamicPrompt("code_review", "Generate code review prompt")
    })
})
```

### MCP Capabilities

| DSL Function | MCP Capability |
|--------------|----------------|
| `Tool(name, desc)` in Method | `tools/list`, `tools/call` |
| `Resource(name, uri, mime)` | `resources/list`, `resources/read` |
| `WatchableResource(...)` | Resources with `resources/subscribe` |
| `StaticPrompt(...)` | `prompts/list`, `prompts/get` (static) |
| `DynamicPrompt(...)` | `prompts/list`, `prompts/get` (dynamic, method-backed) |
| `Notification(...)` | Notification senders |
| `Subscription(...)` | Subscription handlers |
| `SubscriptionMonitor(...)` | SSE subscription monitors |

---

## Registry

`Registry` declares a remote registry source for tool discovery. Registries are centralized
catalogs of MCP servers and toolsets that agents can discover and consume.

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

- `AgentRunPayload`: input for agent run/start/resume endpoints
- `AgentRunResult`: terminal result for non-streaming endpoints
- `AgentRunChunk`: streaming progress events
- Supporting types: `AgentMessage`, `AgentToolEvent`, `AgentToolError`, `AgentRetryHint`, etc.

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
- `config.go` — runtime options bundle; includes `MCPCallers` map when MCP toolsets are used

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
`gen/jsonrpc/<service>/...` and runtime registration helpers in the service package.

---

## Wiring Example

```go
rt := runtime.New(
    runtime.WithEngine(temporalClient),
    runtime.WithMemoryStore(mongoStore),
)

if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{
    Planner: myPlanner,
}); err != nil {
    log.Fatal(err)
}

// MCP toolset wiring
caller := mcp.NewHTTPCaller("https://assistant.example.com/mcp")
if err := mcpassistant.RegisterAssistantToolset(ctx, rt, caller); err != nil {
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

**Let codegen manage MCP registration** — Avoid hand-written glue for consistent codecs.

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
args := tspecs.UnmarshalSearchPayload(call.Payload)
mp, _ := tspecs.ToMethodPayload_Search(args)
result := yourClient.Search(ctx, mp)
tr, _ := tspecs.ToToolReturn_Search(result)
return planner.ToolResult{Result: tr}, nil
```

Notes:
- Compatibility uses Goa's type system (names and structure, including `Extend`)
- For nested shapes, keep pointers in user types for validators/codecs
- Mapping lives in executors; transforms are conveniences when types align
