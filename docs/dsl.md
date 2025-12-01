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

var AssistantSuite = MCPToolset("assistant", "assistant-mcp")

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
- MCP registration helpers when an `MCPToolset` is referenced via `Use`.

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

## Function Reference

### Agent Functions

| Function | Location | Context | Purpose |
|----------|----------|---------|---------|
| `Agent(name, description, dsl)` | `dsl/agent.go` | Inside `Service` | Declares an agent with tool usage/exports and run policy |
| `Use(value, dsl)` | `dsl/agent.go` | Inside `Agent` | Declares that an agent consumes a toolset |
| `Export(value, dsl)` | `dsl/agent.go` | Inside `Agent`/`Service` | Declares toolsets exposed to other agents |
| `AgentToolset(svc, agent, ts)` | `dsl/toolset.go` | Top-level | References a toolset exported by another agent |
| `DisableAgentDocs()` | `dsl/agent.go` | Inside `API` | Disables `AGENTS_QUICKSTART.md` generation |
| `Passthrough(tool, target...)` | `dsl/agent.go` | Inside exported `Tool` | Forwards tool to a Goa service method |

### Toolset Functions

| Function | Location | Context | Purpose |
|----------|----------|---------|---------|
| `Toolset(name, dsl)` | `dsl/toolset.go` | Top-level | Defines a provider-owned toolset |
| `MCPToolset(service, suite, dsl...)` | `dsl/toolset.go` | Top-level | Declares a provider MCP suite |
| `ToolsetDescription(desc)` | `dsl/toolset.go` | Inside `Toolset` | Sets toolset description |
| `Tags(values...)` | `dsl/toolset.go` | Inside `Toolset`/`Tool` | Annotates with metadata tags |

### Tool Functions

| Function | Location | Context | Purpose |
|----------|----------|---------|---------|
| `Tool(name, description, dsl)` | `dsl/toolset.go` | Inside `Toolset`/`Method` | Defines a callable tool |
| `Args(...)` | `dsl/toolset.go` | Inside `Tool` | Defines payload type |
| `Return(...)` | `dsl/toolset.go` | Inside `Tool` | Defines result type |
| `Sidecar(...)` | `dsl/toolset.go` | Inside `Tool` | Defines sidecar type (not sent to model) |
| `BindTo(service?, method)` | `dsl/toolset.go` | Inside `Tool` | Binds tool to a Goa service method |
| `Inject(fields...)` | `dsl/toolset.go` | Inside `Tool` | Marks fields as server-injected (hidden from LLM) |
| `ToolTitle(title)` | `dsl/toolset.go` | Inside `Tool` | Sets human-friendly display title |
| `CallHintTemplate(tmpl)` | `dsl/toolset.go` | Inside `Tool` | Template for call display hint |
| `ResultHintTemplate(tmpl)` | `dsl/toolset.go` | Inside `Tool` | Template for result display hint |

### Policy Functions

| Function | Location | Context | Purpose |
|----------|----------|---------|---------|
| `RunPolicy(dsl)` | `dsl/policy.go` | Inside `Agent` | Configures runtime caps and behavior |
| `DefaultCaps(opts...)` | `dsl/policy.go` | Inside `RunPolicy` | Applies capability caps |
| `MaxToolCalls(n)` | `dsl/policy.go` | Argument to `DefaultCaps` | Max total tool calls |
| `MaxConsecutiveFailedToolCalls(n)` | `dsl/policy.go` | Argument to `DefaultCaps` | Max consecutive failures |
| `TimeBudget(duration)` | `dsl/policy.go` | Inside `RunPolicy` | Max wall-clock execution time |
| `InterruptsAllowed(bool)` | `dsl/policy.go` | Inside `RunPolicy` | Enables run interruption handling |
| `OnMissingFields(action)` | `dsl/policy.go` | Inside `RunPolicy` | Behavior on missing required fields |

### MCP Functions

| Function | Location | Context | Purpose |
|----------|----------|---------|---------|
| `MCPServer(name, version, opts...)` | `dsl/mcp.go` | Inside `Service` | Enables MCP protocol for service |
| `ProtocolVersion(version)` | `dsl/mcp.go` | Option for `MCPServer` | Sets MCP protocol version |
| `MCPTool(name, description)` | `dsl/mcp.go` | Inside `Method` | Marks method as MCP tool |
| `Resource(name, uri, mime)` | `dsl/mcp.go` | Inside `Method` | Marks method as MCP resource |
| `WatchableResource(name, uri, mime)` | `dsl/mcp.go` | Inside `Method` | MCP resource with subscriptions |
| `StaticPrompt(name, desc, msgs...)` | `dsl/mcp.go` | Inside `Service` | Static MCP prompt template |
| `DynamicPrompt(name, description)` | `dsl/mcp.go` | Inside `Method` | Dynamic MCP prompt generator |
| `Notification(name, description)` | `dsl/mcp.go` | Inside `Method` | MCP notification sender |
| `Subscription(resourceName)` | `dsl/mcp.go` | Inside `Method` | Subscription handler for resource |
| `SubscriptionMonitor(name)` | `dsl/mcp.go` | Inside `Method` | SSE monitor for subscriptions |

## Agent, Use, and Export

`Agent` records the service-scoped agent metadata and attaches toolsets via `Use` and `Export`.
Each agent becomes a runtime registration with:

- A workflow definition and Temporal activity handlers
- PlanStart/PlanResume activities with DSL-derived retry/timeout options
- A `Register<Agent>` helper that registers workflows, activities, and toolsets

## Toolset

`Toolset` declares a provider-owned toolset. When declared at top level, the toolset becomes
globally reusable; agents reference it via `Use` and services can expose it via `Export`.

```go
var CommonTools = Toolset("common", func() {
    ToolsetDescription("Shared utility tools")
    Tool("notify", "Send notification", func() {
        Args(func() {
            Attribute("message", String, "Message to send")
            Required("message")
        })
    })
})
```

## MCPToolset

`MCPToolset(service, suite)` declares an MCP-defined toolset. There are two patterns:

**1. Goa-backed MCP server (same design):**

```go
Service("assistant", func() {
    MCPServer("assistant", "1.0.0")
    Method("search", func() {
        Payload(...)
        Result(...)
        MCPTool("search", "Search documents")
    })
})

var AssistantSuite = MCPToolset("assistant", "assistant-mcp")

Agent("chat", "LLM planner", func() {
    Use(AssistantSuite)
})
```

**2. External MCP server (inline schemas):**

```go
var RemoteSearch = MCPToolset("remote", "search", func() {
    Tool("web_search", "Search the web", func() {
        Args(func() { Attribute("query", String) })
        Return(func() { Attribute("results", ArrayOf(String)) })
    })
})

Agent("helper", "", func() {
    Use(RemoteSearch)
})
```

At runtime, supply an `mcpruntime.Caller` for the toolset ID.

## Tool

Each `Tool` describes a callable capability. Compose the payload/result with `Args` and `Return`
using Goa's attribute syntax.

Code generation emits:

- Payload/result Go structs in `specs/types.go`
- JSON codecs in `specs/codecs.go`
- JSON Schema definitions for planners
- Tool registry entries with helper prompts and metadata

### Sidecar (Non-Model Data)

Use `Sidecar` to define structured data attached to tool results that is **not** sent to the model:

```go
Tool("get_time_series", "Get Time Series", func() {
    Args(GetTimeSeriesArgs)
    Return(GetTimeSeriesReturn)       // Model sees this
    Sidecar(GetTimeSeriesSidecar)     // UI/downstream only
})
```

### Display Hint Templates

Use `CallHintTemplate` and `ResultHintTemplate` for UI progress hints:

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

### BindTo (Service Method Binding)

Use `BindTo("Method")` to associate a tool with a service method, or `BindTo("Service", "Method")`
for cross-service bindings. Codegen emits:

- Typed tool specs/codecs under `gen/<svc>/agents/<agent>/specs/<toolset>/`
- `New<Agent><Toolset>ToolsetRegistration(exec runtime.ToolCallExecutor)` helper
- Transform helpers when shapes are compatible:
  - `ToMethodPayload_<Tool>(in <ToolArgs>) (<MethodPayload>, error)`
  - `ToToolReturn_<Tool>(in <MethodResult>) (<ToolReturn>, error)`

### Inject (Server-Side Fields)

Use `Inject` to mark fields as server-injected (hidden from LLM, set by runtime hooks):

```go
Tool("get_data", func() {
    BindTo("data_service", "get")
    Inject("session_id")  // Required by service but hidden from LLM
})
```

### Tool Call IDs

Planners may set `ToolRequest.ToolCallID`. The runtime preserves this ID end-to-end and returns it
in `ToolResult.ToolCallID`. If omitted, the runtime assigns a deterministic ID for replay
correlation.

## RunPolicy, Caps & History

`RunPolicy` configures execution limits and history management enforced at runtime:

```go
RunPolicy(func() {
    DefaultCaps(
        MaxToolCalls(20),
        MaxConsecutiveFailedToolCalls(3),
    )
    TimeBudget("5m")
    InterruptsAllowed(true)
    OnMissingFields("await_clarification")

    History(func() {
        // Simple sliding window: keep only the last 20 turns.
        KeepRecentTurns(20)
    })
})
```

| Option | Values | Purpose |
|--------|--------|---------|
| `MaxToolCalls` | integer | Prevent runaway loops |
| `MaxConsecutiveFailedToolCalls` | integer | Stop on repeated failures |
| `TimeBudget` | duration string | Wall-clock limit |
| `InterruptsAllowed` | boolean | Honor human-in-the-loop interruptions |
| `OnMissingFields` | `""`, `"finalize"`, `"await_clarification"`, `"resume"` | Validation behavior |
| `History` | `History(func)` | Configure how conversation history is bounded |

### History policies

History policies transform the message history before each planner invocation while preserving:

- System prompts at the start of the conversation
- Logical turn boundaries (user + assistant + tool calls/results)

Two standard policies are available:

- **Sliding window** — keep the last N turns:

  ```go
  RunPolicy(func() {
      History(func() {
          // Keep the last 20 user–assistant turns.
          KeepRecentTurns(20)
      })
  })
  ```

- **Compression** — summarize older turns with a model while keeping recent turns in full fidelity:

  ```go
  RunPolicy(func() {
      History(func() {
          // When at least 30 turns exist, summarize older turns and keep
          // the most recent 10 turns as-is.
          Compress(30, 10)
      })
  })
  ```

`Compress(triggerAt, keepRecent)` configures:

- `triggerAt`: minimum total turn count before compression runs.
- `keepRecent`: number of most recent turns to retain in full fidelity.

The generated agent config includes a `HistoryModel` field when `Compress` is used; callers must supply a `model.Client`. At runtime, the history policy uses this client with `ModelClassSmall` to summarize older turns.

## MCP Server Definition

Enable MCP protocol for a service with `MCPServer`:

```go
Service("calculator", func() {
    MCPServer("calc", "1.0.0", ProtocolVersion("2025-06-18"))

    Method("add", func() {
        Payload(func() {
            Attribute("a", Int)
            Attribute("b", Int)
        })
        Result(func() {
            Attribute("sum", Int)
        })
        MCPTool("add", "Add two numbers")
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
})
```

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

### 4. MCP Helpers (`gen/<svc>/mcp_<service>/`)

- `register.go` — `Register<Service><Suite>Toolset` functions
- `client/caller.go` — `NewCaller` helper for Goa-generated MCP clients

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
out, err := client.Run(ctx, messages, runtime.WithSessionID("session-1"))
```

## Best Practices

- **Keep tool descriptions concise** — Generated helper prompts reuse this text.
- **Reuse toolsets** — `var Shared = Toolset("shared", ...)` avoids duplication.
- **Use specific validations** — Required, MinLength, Enum give planners strong schemas.
- **Treat run policies as API contracts** — Choose bounds that match downstream SLAs.
- **Use `BoundedResult()` for large views** — Mark tools that return potentially large
  lists, graphs, or windows as bounded. When `BoundedResult()` is set on a `Tool`,
  codegen:
  - sets `BoundedResult: true` on the generated `tools.ToolSpec`,
  - extends the tool result alias type with a `Bounds *agent.Bounds` field
    (JSON `bounds` property) so models and `tool_schemas.json` see canonical
    truncation metadata, and
  - generates a `ResultBounds() agent.Bounds` method so result types implement
    `agent.BoundedResult` without requiring reflection in the runtime.
- **Let codegen manage MCP registration** — Avoid hand-written glue for consistent codecs.
- **Use display hint templates** — `CallHintTemplate` and `ResultHintTemplate` improve UI feedback.
- **Use Sidecar for full-fidelity data** — Keep model payloads bounded; attach artifacts via Sidecar.

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
