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

### Cross‑Process Inline Composition (Auto‑Wiring)

When agent A "uses" a toolset exported by agent B, Goa‑AI wires composition automatically:

- The exporter (agent B) package includes a generated `Register<Agent>Route(ctx, rt)` function
  that registers route‑only metadata.
- The consumer (agent A) registry calls `Register<AgentB>Route(ctx, rt)` and registers an inline
  agent‑tool `ToolsetRegistration`. The generated Execute function calls
  `runtime.ExecuteAgentInline` so the nested agent runs as part of the parent workflow history.
- Payloads and results remain canonical JSON across boundaries and are decoded exactly once.

This yields a single deterministic workflow and unified `tool_start`/`tool_result` events, even
when agents execute on different workers.

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
| `BoundedResult()` | Inside `Tool` | Marks result as bounded view over larger data |
| `ResultReminder(text)` | Inside `Tool` | Static system reminder injected after tool result |

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

### A2A Functions

| Function | Context | Purpose |
|----------|---------|---------|
| `FromA2A(suite, url)` | Argument to `Toolset` | Configure toolset backed by remote A2A provider |
| `A2A(func())` | Inside `Export` toolset | Configure A2A-specific settings for export |
| `Suite(id)` | Inside `A2A` | Override default A2A suite identifier |
| `A2APath(path)` | Inside `A2A` | Override default A2A HTTP path (default: "/a2a") |
| `A2AVersion(version)` | Inside `A2A` | Override A2A protocol version (default: "1.0") |

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
- Result type includes a `Bounds *agent.Bounds` field
- Generated `ResultBounds()` method implements `agent.BoundedResult`
- Runtime attaches bounds metadata to hook events and streams

```go
Tool("list_devices", "List devices in scope", func() {
    Args(ListDevicesArgs)
    Return(ListDevicesReturn)
    BoundedResult()  // Result may be truncated
})
```

Services are responsible for trimming and setting `Bounds.Truncated`, `Bounds.Total`, etc.

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

## A2A Protocol

A2A (Agent-to-Agent) is Google's open standard for agent interoperability. Goa-AI supports
both consuming remote A2A agents and exposing agents as A2A providers.

### A2A-Backed Toolsets

Use `FromA2A` to configure a toolset backed by a remote A2A provider:

```go
// Basic A2A-backed toolset (name derived from suite)
var RemoteTools = Toolset(FromA2A("svc.agent.tools", "https://provider.example.com"))

// With explicit name
var RemoteTools = Toolset("remote-tools", FromA2A("svc.agent.tools", "https://provider.example.com"))

// Use in an agent
Agent("orchestrator", "Main coordinator", func() {
    Use(RemoteTools)
})
```

### A2A Export Configuration

Configure A2A-specific settings for exported toolsets using the `A2A` block:

```go
Agent("specialist", "Domain expert", func() {
    Export("analysis", func() {
        Tool("analyze", "Perform deep analysis", func() {
            Args(AnalysisRequest)
            Return(AnalysisResult)
        })
        A2A(func() {
            Suite("custom.suite.identifier")  // Override default suite ID
            A2APath("/custom-a2a")            // Override HTTP path (default: "/a2a")
            A2AVersion("1.1")                 // Override protocol version (default: "1.0")
        })
    })
})
```

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

### 1. Agent Package (`gen/<svc>/agents/<agent>/`)

- `agent.go` — registers workflows/activities/toolsets; exports `const AgentID agent.Ident`
- `workflow.go` — implements the durable run loop
- `activities.go` — thin wrappers calling runtime activities
- `config.go` — runtime options bundle; includes `MCPCallers` map when MCP toolsets are used

### 2. Tool Specs (`specs/`)

- `types.go`, `codecs.go`, `specs.go` — payload/result structs, codecs, registry entries

### 3. Agent Tool Exports (`agenttools/`)

- Helper constructors for exported toolsets
- Typed tool identifiers as `tools.Ident` constants

### 4. MCP Helpers (`gen/mcp_<service>/`)

- `register.go` — `Register<Service><Suite>Toolset` functions
- `client/caller.go` — `NewCaller` helper for Goa-generated MCP clients

### 5. Tool Schemas JSON

Every agent gets a backend-agnostic JSON catalogue at
`gen/<service>/agents/<agent>/specs/tool_schemas.json`:

```json
{
  "tools": [
    {
      "id": "toolset.tool",
      "service": "orchestrator",
      "toolset": "helpers",
      "title": "Answer a simple question",
      "description": "Answer a simple question",
      "tags": ["chat"],
      "payload": { "name": "Ask", "schema": { /* JSON Schema */ } },
      "result": { "name": "Answer", "schema": { /* JSON Schema */ } }
    }
  ]
}
```

---

## Wiring Example

```go
rt := runtime.New(
    runtime.WithEngine(temporalClient),
    runtime.WithMemoryStore(mongoStore),
    runtime.WithRunStore(runStore),
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
