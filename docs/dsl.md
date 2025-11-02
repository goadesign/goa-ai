# Goa Agent DSL Reference

This document explains how to author agents, toolsets, and runtime policies with the Goa agents DSL. Use it alongside `docs/plan.md`, which describes the broader architecture, runtime, and code generation pipeline.

## Overview

- **Import path:** add `agentsdsl "goa.design/goa-ai/agents/dsl"` to your Goa design packages.
- **Entry point:** declare agents inside a regular Goa `Service` definition. The DSL augments Goa’s design tree and is processed during `goa gen`.
- **Outcome:** `goa gen` produces agent packages (`gen/<service>/agents/<agent>`), tool codecs/specs, activity handlers, and registration helpers that wire the design into the runtime. A contextual `AGENTS_QUICKSTART.md` is written at the module root unless disabled via `agentsdsl.DisableAgentDocs()`.

The DSL is evaluated by Goa’s `eval` engine, so the same rules apply as with the standard service/transport DSL: expressions must be invoked in the proper context, and attribute definitions reuse Goa’s type system (`Attribute`, `Field`, validations, examples, etc.).

## Quickstart

```go
package design

import (
	. "goa.design/goa/v3/dsl"
	agentsdsl "goa.design/goa-ai/agents/dsl"
)

var DocsToolset = agentsdsl.Toolset("docs.search", func() {
	agentsdsl.Tool("search", "Search indexed documentation", func() {
		agentsdsl.Args(func() {
			Attribute("query", String, "Search phrase")
			Attribute("limit", Int, "Max results", func() { Default(5) })
			Required("query")
		})
		agentsdsl.Return(func() {
			Attribute("documents", ArrayOf(String), "Matched snippets")
			Required("documents")
		})
		agentsdsl.Tags("docs", "search")
	})
})

var _ = Service("orchestrator", func() {
	Description("Human front door for the knowledge agent.")

        agentsdsl.Agent("chat", "Conversational runner", func() {
		agentsdsl.Uses(func() {
			agentsdsl.Toolset(DocsToolset)
			agentsdsl.UseMCPToolset("assistant", "assistant-mcp")
		})
		agentsdsl.Exports(func() {
			agentsdsl.Toolset("chat.tools", func() {
				agentsdsl.Tool("summarize_status", "Produce operator-ready summaries", func() {
					agentsdsl.Args(func() {
						Attribute("prompt", String, "User instructions")
						Required("prompt")
					})
					agentsdsl.Return(func() {
						Attribute("summary", String, "Assistant response")
						Required("summary")
					})
					agentsdsl.Tags("chat")
				})
			})
		})
		agentsdsl.RunPolicy(func() {
			agentsdsl.DefaultCaps(
				agentsdsl.MaxToolCalls(8),
				agentsdsl.MaxConsecutiveFailedToolCalls(3),
			)
			agentsdsl.TimeBudget("2m")
		})
	})
})
```

Running `goa gen example.com/assistant/design` now produces:

- `gen/orchestrator/agents/chat`: workflow + planner activities + agent registry.
- `gen/orchestrator/agents/chat/tool_specs`: payload/result structs, JSON codecs, tool schemas.
- `gen/orchestrator/agents/chat/agenttools`: helpers that expose exported tools to other agents.
- `features/mcp`-aware registration helpers when `UseMCPToolset` is invoked.

## Function Reference

| Function | Location | Context | Purpose |
|----------|----------|---------|---------|
| `Agent(name, description, dsl)` | `dsl/agent.go` | Inside `Service` | Declares an agent, its tool usage/exports, and run policy. |
| `Uses(dsl)` | `dsl/agent.go` | Inside `Agent` | Describes toolsets the agent consumes. |
| `Exports(dsl)` | `dsl/agent.go` | Inside `Agent` | Declares toolsets exposed to other agents. |
| `Toolset(nameOrExpr, dsl)` | `dsl/toolset.go` | Top-level or inside `Uses` / `Exports` | Defines or references a set of tools. |
| `UseMCPToolset(service, suite)` | `dsl/toolset.go` | Inside `Uses` | Pulls in a toolset that originated from a Goa-declared MCP server. |
| `Tool(name, description, dsl)` | `agents/dsl/toolset.go` | Inside `Toolset` | Defines a tool (arguments, result, metadata). |
| `Args(...)`, `Return(...)` | `agents/dsl/toolset.go` | Inside `Tool` | Define payload/result types using the standard Goa attribute DSL. |
| `Tags(values...)` | `agents/dsl/toolset.go` | Inside `Tool` | Annotates tools with metadata tags. |
| `RunPolicy(dsl)` | `agents/dsl/policy.go` | Inside `Agent` | Configures runtime caps and behavior. |
| `DefaultCaps(opts...)` | `agents/dsl/policy.go` | Inside `RunPolicy` | Applies capability caps (max calls, consecutive failures). |
| `MaxToolCalls`, `MaxConsecutiveFailedToolCalls` | `agents/dsl/policy.go` | Arguments to `DefaultCaps` | Helper options for caps. |
| `TimeBudget(duration)` | `agents/dsl/policy.go` | Inside `RunPolicy` | Sets max wall-clock execution time. |
| `InterruptsAllowed(bool)` | `agents/dsl/policy.go` | Inside `RunPolicy` | Enables run interruption handling. |
| `DisableAgentDocs()` | `agents/dsl/docs.go` | Inside `API` | Disables generation of `AGENTS_QUICKSTART.md` at the module root. |

### Agent, Uses, and Exports

`Agent` records the service-scoped agent metadata (name, description, owning service) and attaches toolset groups via `Uses` and `Exports`. Each agent becomes a runtime registration with:

- A workflow definition and Temporal activity handlers.
- PlanStart/PlanResume activities with DSL-derived retry/timeout options.
- A `Register<Agent>` helper that registers workflows, activities, and toolsets against a `runtime.Runtime`.

### Toolset

`Toolset` either declares a new toolset (`Toolset("name", func(){...})`) or references a previously declared toolset (`Toolset(SharedToolsetExpr)`). When declared at top level, the toolset becomes globally reusable; when declared inside `Exports`, it will be published as agent tools via generated helpers (`agenttools` package). Toolsets can carry multiple tools, each with payload/result schemas, helper prompts, and metadata tags.

### UseMCPToolset

`UseMCPToolset(service, suite)` injects an MCP-defined toolset into the current agent’s `Uses` block. During code generation the DSL is resolved to the service-level MCP metadata, and the agent package automatically imports the generated helper (`Register<Service><Suite>Toolset`) and exposes an `MCPCallers` map in the agent config. At runtime:

1. Application code instantiates an `mcpruntime.Caller` (HTTP, SSE, stdio, or the Goa-generated JSON-RPC caller) and stores it under the toolset ID constant (e.g., `ChatAgentAssistantAssistantMcpToolsetID`).
2. The agent registry automatically invokes the generated helper for each configured caller, registering the MCP suite with the runtime.
3. Planner outputs can now reference the tools by their names just like native toolsets; codecs, schemas, retry hints, and structured telemetry flow through the shared runtime path.

### Tool

Each `Tool` describes a callable capability. Compose the payload/result with `Args` and `Return` using Goa’s attribute syntax (attributes, types, validations, examples). Tags are optional metadata strings surfaced to policy engines and telemetry.

Code generation emits:

- Payload/result Go structs in `tool_specs/types.go`.
- JSON codecs (`tool_specs/codecs.go`) used for activity marshaling and memory.
- JSON Schema definitions consumed by planners and optional validation layers.
- Tool registry entries consumed by the runtime, including helper prompts and metadata.

#### BindTo(service, method)

Use `BindTo("Method")` to associate a tool with a service method on the current
service, or `BindTo("Service", "Method")` for cross‑service bindings. During
evaluation the DSL resolves the referenced `*expr.MethodExpr`; codegen then
emits:

- Typed tool specs/codecs under `gen/<svc>/agents/<agent>/specs/<toolset>/`.
- A `New<Agent><Toolset>ToolsetRegistration(exec runtime.ToolCallExecutor)`
  helper. Application code registers the toolset by supplying an executor
  function (see docs/runtime.md for the executor‑first model).
- When shapes are compatible, per‑tool transform helpers in
  `specs/<toolset>/transforms.go`:
  - `ToMethodPayload_<Tool>(in <ToolArgs>) (<MethodPayload>, error)`
  - `ToToolReturn_<Tool>(in <MethodResult>) (<ToolReturn>, error)`

Mapping logic lives in your application‑owned executor stubs under
`internal/agents/<agent>/toolsets/<toolset>/execute.go` (generated by
`goa example`). Executors decode typed payloads, optionally call the generated
transforms, invoke your service client, and return a `planner.ToolResult`.

#### Tool Call IDs (Model Correlation)

Planners may set `ToolRequest.ToolCallID` (e.g., model `tool_call.id`). The runtime preserves this ID end-to-end and returns it in `ToolResult.ToolCallID`. If omitted, the runtime assigns a deterministic ID so workflow replay correlates correctly.

### RunPolicy & Caps

`RunPolicy` configures execution limits enforced at runtime:

- `DefaultCaps` with `MaxToolCalls` and `MaxConsecutiveFailedToolCalls` prevent runaway loops.
- `TimeBudget` enforces a wall-clock limit; the runtime monitors elapsed time and aborts when exceeded.
- `InterruptsAllowed` signals to the runtime that human-in-the-loop interruptions should be honored.

These values appear in the generated workflow configuration and the runtime enforces them on every turn.

### Agent API Types (re‑exported)

The DSL re‑exports standardized agent API types for use directly in Goa service designs. Import the agents DSL and reference these types when declaring agent endpoints:

- `AgentRunPayload`: input for agent run/start/resume endpoints (fields: `agent_id`, `run_id`, `session_id`, `turn_id`, `messages`, `labels`, `metadata`).
- `AgentRunResult`: terminal result for non‑streaming endpoints (fields: `agent_id`, `run_id`, `final`, `tool_events`, `notes`).
- `AgentRunChunk`: streaming progress events (variants via fields: `message`, `tool_call`, `tool_result`, `status`).
- Supporting types: `AgentMessage`, `AgentToolEvent`, `AgentToolError`, `AgentRetryHint`, `AgentToolTelemetry`, `AgentPlannerAnnotation`, `AgentToolCallChunk`, `AgentToolResultChunk`, `AgentRunStatusChunk`.

Example:

```go
Service("orchestrator", func() {
    Method("run", func() {
        Payload(agentsdsl.AgentRunPayload)
        StreamingResult(agentsdsl.AgentRunChunk)
        JSONRPC(func() { ServerSentEvents(func() {}) })
    })
    Method("run_sync", func() {
        Payload(agentsdsl.AgentRunPayload)
        Result(agentsdsl.AgentRunResult)
    })
})
```

These types map to runtime/planner types via generated conversions (`ConvertTo*`/`CreateFrom*`) and should be used only at API boundaries. Inside planners and runtime code, prefer the runtime `planner.*` types.

## Generated Artifacts (Design → Code)

For each service/agent combination, `goa gen` produces:

1. **Agent package (`gen/<svc>/agents/<agent>/`)**
   - `agent.go` registers workflows/activities/toolsets with `runtime.Runtime`.
   - `agent.go` also exports `const AgentID agent.Ident = "<service>.<agent>"` for type‑safe references.
   - `workflow.go` implements the durable run loop (start, execute tools, resume).
   - `activities.go` exposes thin wrappers that call into `runtime.PlanStartActivity`, `PlanResumeActivity`, and `ExecuteToolActivity`.
   - `config.go` bundles runtime options (planner implementation, task queues, retry policies). When MCP toolsets are used, the config includes an `MCPCallers` map and a builder `WithMCPCaller(id string, caller mcpruntime.Caller)` to simplify wiring.
2. **Tool specs (`tool_specs/`)**
   - `types.go`, `codecs.go`, `specs.go` as described above.
3. **Agent tool exports (`agenttools/`)**
   - Helper constructors for exported toolsets, ready to be registered with other agents (`Register<Service><Toolset>`).
   - Content configuration is optional: if you do not provide per‑tool text or templates, the runtime builds a reasonable default user message from the tool payload. You can override the default builder via `WithPromptBuilder`.
   - Each exported toolset also defines typed tool identifiers as `tools.Ident` constants you can reference anywhere. For example:
     ```go
     import chattools "example.com/assistant/gen/orchestrator/agents/chat/agenttools/search"

     // Use a generated constant instead of ad‑hoc strings/casts
     spec, _ := rt.ToolSpec(chattools.Search)
     schemas, _ := rt.ToolSchema(chattools.Search)

     // Build a typed tool call using only agenttools aliases (no specs import needed)
     req := chattools.NewFindCall(&chattools.FindPayload{
         // fill fields per your design
     }, chattools.WithToolCallID("tc-1"))
     _ = req
     ```
4. **MCP helpers (`gen/<svc>/mcp_<service>/register.go`, `client/caller.go`)**
   - `Register<Service><Suite>Toolset` functions that adapt MCP tool metadata into runtime registrations.
   - `client.NewCaller` helper so Goa-generated MCP clients can be plugged directly into the runtime.

Consumers import these generated packages inside their worker/binary setup code:

```go
rt := runtime.New(
    runtime.WithEngine(temporalClient),
    runtime.WithMemoryStore(mongoStore),
    runtime.WithRunStore(runStore),
)
if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{Planner: myPlanner}); err != nil {
	log.Fatal(err)
}
caller := featuresmcp.NewHTTPCaller("https://assistant.example.com/mcp")
if err := mcpassistant.RegisterAssistantAssistantMcpToolset(ctx, rt, caller); err != nil {
	log.Fatal(err)
}

// Execute the agent using the runtime client and the typed AgentID constant.
messages := []planner.AgentMessage{{Role: "user", Content: "Say hi"}}
client := chat.NewClient(rt)
out, err := client.Run(ctx, messages, runtime.WithSessionID("session-1"))
if err != nil { log.Fatal(err) }
_ = out // *runtime.RunOutput
```

## MCP Bridge Workflow

1. **Service design**: declare the MCP server via Goa’s MCP DSL (unchanged from legacy goa-ai). This records tool/resource/prompt metadata.
2. **Agent design**: reference that suite with `UseMCPToolset("service", "suite")`.
3. **Code generation**: produces both the classic MCP JSON-RPC server (optional) and the runtime registration helper, plus tool codecs/specs mirrored into the agent package.
4. **Runtime wiring**: instantiate an `mcpruntime.Caller` transport (HTTP/SSE/stdio). Generated helpers register the toolset and adapt JSON-RPC errors into `planner.RetryHint` values.
5. **Planner execution**: planners simply enqueue tool calls; the runtime automatically encodes payloads, invokes the MCP caller, persists results via hooks, and surfaces structured telemetry.

The example runtime harness (`example/complete/runtime_harness.go`) shows the flow end-to-end without Temporal: it registers the generated helper, uses a stubbed MCP caller, and executes the chat workflow fully in-process for tests and documentation.

## Best Practices

- Keep tool descriptions concise and action-oriented—the generated helper prompts reuse this text.
- Reuse toolsets via `var Shared = Toolset("shared", ...)` to avoid duplication across agents.
- Prefer specific validations (Required, MinLength, Enum) so planners receive strong schemas; the runtime trusts these contracts.
- Treat run policies as part of your API contract: choose bounds that match downstream SLAs and enforce them consistently (policy engines can still override at runtime).
- For MCP integrations, let the codegen-managed helper register the toolset; avoid hand-written glue so codecs and retry hints stay consistent with future updates.
- When planners require incremental LLM output, fetch the registered model via `AgentContext.Model()`, set `model.Request.Stream = true`, and (optionally) specify `model.ThinkingOptions`. Bedrock adapters translate these hints into ConverseStream requests; providers that do not support streaming return `model.ErrStreamingUnsupported`, so planners can fall back to unary completions.

For deeper architectural context (workflow engine interface, runtime package layout, feature modules), see the comprehensive roadmap in `docs/plan.md` and the upcoming `docs/runtime.md`.

## Transforms and Compatibility (BindTo)

When a tool is bound to a Goa method via `BindTo`, code generation analyzes the
tool Arg/Return and the method Payload/Result. If the shapes are compatible,
Goa emits type‑safe transform helpers in
`gen/<svc>/agents/<agent>/specs/<toolset>/transforms.go`:

- `ToMethodPayload_<Tool>(in <ToolArgs>) (<MethodPayload>, error)`
- `ToToolReturn_<Tool>(in <MethodResult>) (<ToolReturn>, error)`

Use these helpers inside your executor implementation to avoid boilerplate. If
shapes are not compatible, write explicit field mappings in the executor.

Notes:
- Compatibility uses Goa’s type system (names and structure, including
  `Extend`). For non‑primitive/nested shapes, keep pointers in user types so
  validators and codecs work as expected.
- No adapters are generated in core. Mapping lives in executors; transforms are
  conveniences when types align.

Example (compatible types → transforms emitted):

```
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
        Uses(func() {
            Toolset("ts", func() {
                Tool("search", "", func() {
                    Args(SearchPayload)      // aliases method payload
                    Return(SearchResult)     // aliases method result
                    BindTo("svc", "Search")
                })
            })
        })
    })
})
```

Generated transforms live under `specs/ts/transforms.go`. In your executor
stub (`internal/agents/a/toolsets/ts/execute.go`) you can:

```go
// args := tspecs.UnmarshalSearchPayload(call.Payload)
mp, _ := tspecs.ToMethodPayload_Search(args)
// result := yourClient.Search(ctx, mp)
tr, _ := tspecs.ToToolReturn_Search(result)
return planner.ToolResult{Payload: tr}, nil
```
