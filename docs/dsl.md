# Goa Agent DSL Reference

This document explains how to author agents, toolsets, and runtime policies with the Goa agents DSL. Use it alongside `docs/plan.md`, which describes the broader architecture, runtime, and code generation pipeline.

## Overview

- **Import path:** add `agentsdsl "goa.design/goa-ai/agents/dsl"` to your Goa design packages.
- **Entry point:** declare agents inside a regular Goa `Service` definition. The DSL augments Goa’s design tree and is processed during `goa gen`.
- **Outcome:** `goa gen` produces agent packages (`gen/<service>/agents/<agent>`), tool codecs/specs, activity handlers, and registration helpers that wire the design into the runtime.

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
| `Agent(name, description, dsl)` | `agents/dsl/agent.go` | Inside `Service` | Declares an agent, its tool usage/exports, and run policy. |
| `Uses(dsl)` | `agents/dsl/agent.go` | Inside `Agent` | Describes toolsets the agent consumes. |
| `Exports(dsl)` | `agents/dsl/agent.go` | Inside `Agent` | Declares toolsets exposed to other agents. |
| `Toolset(nameOrExpr, dsl)` | `agents/dsl/toolset.go` | Top-level or inside `Uses` / `Exports` | Defines or references a set of tools. |
| `UseMCPToolset(service, suite)` | `agents/dsl/toolset.go` | Inside `Uses` | Pulls in a toolset that originated from a Goa-declared MCP server. |
| `Tool(name, description, dsl)` | `agents/dsl/toolset.go` | Inside `Toolset` | Defines a tool (arguments, result, metadata). |
| `Args(...)`, `Return(...)` | `agents/dsl/toolset.go` | Inside `Tool` | Define payload/result types using the standard Goa attribute DSL. |
| `Tags(values...)` | `agents/dsl/toolset.go` | Inside `Tool` | Annotates tools with metadata tags. |
| `RunPolicy(dsl)` | `agents/dsl/policy.go` | Inside `Agent` | Configures runtime caps and behavior. |
| `DefaultCaps(opts...)` | `agents/dsl/policy.go` | Inside `RunPolicy` | Applies capability caps (max calls, consecutive failures). |
| `MaxToolCalls`, `MaxConsecutiveFailedToolCalls` | `agents/dsl/policy.go` | Arguments to `DefaultCaps` | Helper options for caps. |
| `TimeBudget(duration)` | `agents/dsl/policy.go` | Inside `RunPolicy` | Sets max wall-clock execution time. |
| `InterruptsAllowed(bool)` | `agents/dsl/policy.go` | Inside `RunPolicy` | Enables run interruption handling. |

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

### RunPolicy & Caps

`RunPolicy` configures execution limits enforced at runtime:

- `DefaultCaps` with `MaxToolCalls` and `MaxConsecutiveFailedToolCalls` prevent runaway loops.
- `TimeBudget` enforces a wall-clock limit; the runtime monitors elapsed time and aborts when exceeded.
- `InterruptsAllowed` signals to the runtime that human-in-the-loop interruptions should be honored.

These values appear in the generated workflow configuration and the runtime enforces them on every turn.

## Generated Artifacts (Design → Code)

For each service/agent combination, `goa gen` produces:

1. **Agent package (`gen/<svc>/agents/<agent>/`)**
   - `agent.go` registers workflows/activities/toolsets with `runtime.Runtime`.
   - `workflow.go` implements the durable run loop (start, execute tools, resume).
   - `activities.go` exposes thin wrappers that call into `runtime.PlanStartActivity`, `PlanResumeActivity`, and `ExecuteToolActivity`.
   - `config.go` bundles runtime options (planner implementation, task queues, retry policies).
2. **Tool specs (`tool_specs/`)**
   - `types.go`, `codecs.go`, `specs.go` as described above.
3. **Agent tool exports (`agenttools/`)**
   - Helper constructors for exported toolsets, ready to be registered with other agents (`Register<Service><Toolset>`).
4. **MCP helpers (`gen/<svc>/mcp_<service>/register.go`, `client/caller.go`)**
   - `Register<Service><Suite>Toolset` functions that adapt MCP tool metadata into runtime registrations.
   - `client.NewCaller` helper so Goa-generated MCP clients can be plugged directly into the runtime.

Consumers import these generated packages inside their worker/binary setup code:

```go
rt := runtime.New(runtime.Options{Engine: temporalClient, MemoryStore: mongoStore, RunStore: runStore})
if err := chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{Planner: myPlanner}); err != nil {
	log.Fatal(err)
}
caller := featuresmcp.NewHTTPCaller("https://assistant.example.com/mcp")
if err := mcpassistant.RegisterAssistantAssistantMcpToolset(ctx, rt, caller); err != nil {
	log.Fatal(err)
}
```

## MCP Bridge Workflow

1. **Service design**: declare the MCP server via Goa’s MCP DSL (unchanged from legacy goa-ai). This records tool/resource/prompt metadata.
2. **Agent design**: reference that suite with `UseMCPToolset("service", "suite")`.
3. **Code generation**: produces both the classic MCP JSON-RPC server (optional) and the runtime registration helper, plus tool codecs/specs mirrored into the agent package.
4. **Runtime wiring**: instantiate an `mcpruntime.Caller` transport (HTTP/SSE/stdio). Generated helpers register the toolset and adapt JSON-RPC errors into `planner.RetryHint` values.
5. **Planner execution**: planners simply enqueue tool calls; the runtime automatically encodes payloads, invokes the MCP caller, persists results via hooks, and surfaces structured telemetry.

The example runtime harness (`example/runtime_harness.go`) shows the flow end-to-end without Temporal: it registers the generated helper, uses a stubbed MCP caller, and executes the chat workflow fully in-process for tests and documentation.

## Best Practices

- Keep tool descriptions concise and action-oriented—the generated helper prompts reuse this text.
- Reuse toolsets via `var Shared = Toolset("shared", ...)` to avoid duplication across agents.
- Prefer specific validations (Required, MinLength, Enum) so planners receive strong schemas; the runtime trusts these contracts.
- Treat run policies as part of your API contract: choose bounds that match downstream SLAs and enforce them consistently (policy engines can still override at runtime).
- For MCP integrations, let the codegen-managed helper register the toolset; avoid hand-written glue so codecs and retry hints stay consistent with future updates.
- When planners require incremental LLM output, fetch the registered model via `AgentContext.Model()`, set `model.Request.Stream = true`, and (optionally) specify `model.ThinkingOptions`. Bedrock adapters translate these hints into ConverseStream requests; providers that do not support streaming return `model.ErrStreamingUnsupported`, so planners can fall back to unary completions.

For deeper architectural context (workflow engine interface, runtime package layout, feature modules), see the comprehensive roadmap in `docs/plan.md` and the upcoming `docs/runtime.md`.
