# Chat Data Loop Example

This walkthrough explains how the example under `example/complete/` wires the full Goa-AI stack together:

- Agent + planner defined via the DSL
- Runtime harness (in-process engine) for deterministic tests
- Temporal adapter for durable execution
- Mongo-backed memory/run stores + Pulse stream sink
- MCP toolset exported by the assistant service and consumed by the orchestrator agent

Use this document alongside the source files:

| File | Purpose |
| --- | --- |
| `example/complete/design/design.go` | Goa + agent DSL definitions (chat agent + MCP suite) |
| `example/complete/chat_planner.go` | Sample planner implementing `planner.Planner` |
| `example/complete/runtime_harness.go` | In-process engine to run workflows without Temporal |
| `example/complete/runtime_example_test.go` | End-to-end test driving the harness |
| `example/complete/mcp_assistant.go` | JSON-RPC server exposing the MCP suite |
| `example/complete/orchestrator.go` | Temporal worker bootstrap for the chat agent |

---

## 1. Generate the example

From the repository root:

```bash
go generate ./example/complete/...   # if you add go:generate hooks
cd example
goa gen example.com/assistant/design
goa example example.com/assistant/design
```

This populates `gen/` with:

- `gen/orchestrator/agents/chat/...` (agent package, workflows, activities)
- `gen/orchestrator/agents/chat/tool_specs/...` (tool schemas + codecs)
- `gen/assistant/mcp/...` (JSON-RPC server + client helpers)

> **Tip:** `integration_tests/tests` re-run `goa gen` automatically, so keep the design in sync before running `go test ./...`.

## 2. Run the runtime harness

The harness instantiates an in-memory engine that implements `engine.Engine` without Temporal. It’s ideal for local tests and documentation.

```bash
go test ./example -run TestRuntimeHarness -v
```

What happens:

1. `registerHarness` (in `runtime_harness.go`) builds:
   - In-memory memory/session stores
   - Pulse capture sink (records events instead of publishing)
   - Runtime with noop telemetry
2. Generated helper `RegisterChatAgent` registers the agent, planner, toolsets, and activities.
3. The harness queues a run with two user messages; the planner issues an MCP call.
4. `runtime_example_test.go` asserts:
   - Planner receives memory snapshot + run context
   - MCP tool result is decoded via generated codecs
   - Hooks emit stream events (sequence checks)

You can extend the test to inspect recorded stream events (see `captureSink` in `runtime_harness.go`).

## 3. Bring up the MCP assistant

The assistant service exposes an MCP suite (`assistant-mcp`) consumed by the orchestrator. Start it with the generated server + adapter:

```bash
go run ./example/complete/cmd/assistant -http-port 8080
```

Key pieces:

- `example/complete/mcp_assistant.go` wires the generated Goa transport to `mcpassistant.NewMCPAdapter`.
- `RegisterAssistantAssistantMcpToolset` is invoked in `orchestrator.go` so the orchestration runtime knows about the external tools.
- The adapter captures structured tool output (`ToolResult.Telemetry["structured"]`) so downstream policy/observability layers can differentiate plain text vs structured payloads.

## 4. Run the Temporal worker + orchestrator

With the MCP server running:

```bash
# start Temporal worker that registers the chat agent
TEMPORAL_NAMESPACE=default go run ./example/complete/cmd/orchestrator \
  -temporal-address 127.0.0.1:7233 \
  -mongo-uri mongodb://localhost:27017 \
  -redis-uri redis://localhost:6379
```

What `cmd/orchestrator` does:

1. Creates Temporal client + engine via `agents/runtime/engine/temporal`.
2. Instantiates Mongo-backed memory/run stores (`features/memory/mongo`, `features/run/mongo`).
3. Creates Pulse sink (`features/stream/pulse`) for emitting hook events.
4. Calls `RegisterChatAgent` and `RegisterAssistantAssistantMcpToolset` to hook planners + MCP suite.
5. Starts workers (adapter auto-starts once runs begin, but the command also calls `Worker().Start()` for explicit control).

From there you can invoke the orchestrator via HTTP/gRPC (Goa transports) or through tests that call `runtime.Run` with the Temporal engine.

## 5. Customize & extend

- **Planner logic:** edit `chat_planner.go` to change prompt templates, retry hints, or model usage. Because planners implement `planner.Planner`, nothing else needs to change.
- **Additional toolsets:** add more `Toolset` definitions in the DSL; regenerate to get codecs + registries.
- **Agent as tool:** wrap toolsets inside `Exports` so other agents can call them (`Register<Agent>Toolset` helpers are generated automatically).
- **Memory/stream backends:** swap Mongo/Pulse for alternatives by implementing the `memory.Store` or `stream.Sink` interfaces and passing them to `runtime.New`.

## 6. Debugging tips

- Enable Clue debug logging by injecting a context created with `log.Context` before calling `runtime.StartRun`.
- Use `hooks.NewStreamSubscriber` with a custom sink (e.g., stdout) to inspect runtime events.
- Temporal traces flow through OTEL interceptors by default; configure OTEL exporters (e.g., Jaeger, OTLP) via environment variables before starting the worker.

---

This single example exercises every major subsystem—DSL, codegen, runtime, Temporal adapter, Mongo memory, Pulse streaming, and MCP integration—serving as the canonical reference when porting real services.
