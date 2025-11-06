# Migration: From Ad‑Hoc Bridges to DSL‑Driven Inline Agent Tools

This guide helps migrate code that manually bridged agent‑as‑tool execution
(e.g., calling `client.Run` from inside workflow code) to the DSL‑driven,
inline composition model.

## Before

- Toolset executors invoked remote clients (`client.Run`) from workflow code.
- Missing `ToolResult` frames (no `tool_end`) due to network I/O inside workflows.
- Ad‑hoc payload coercion (`map[string]any`) and double encoding.

## After

- Exporter registers route‑only metadata via generated `Register<Agent>Route(ctx, rt)`.
- Consumer auto‑wires toolset registration with `Inline = true` and generated `Execute`
  that calls `runtime.ExecuteAgentInline`.
- The runtime schedules nested `Plan`/`Resume` as activities; no network in workflow code.
- Canonical JSON (`json.RawMessage`) flows end‑to‑end and decodes exactly once using
  generated codecs at the tool boundary.
- A single stream of `tool_start` / `tool_result` events is emitted by the parent runtime.

## Steps

1. Remove bridge executors that call `client.Run` from workflow code.
2. Ensure the exporter is on the module path so the consumer can import and call
   `Register<Agent>Route(ctx, rt)`.
3. Regenerate code (`goa gen`): exporter gains the route registration; consumer gains
   auto‑wiring for route and inline toolset registration.
4. Verify SSE includes both `tool_call` and `tool_result` frames; no planner/tool calls
   occur directly in workflow code.

## Notes

- Route‑only registration is idempotent and can coexist with full local registration.
- Policies (caps, time budget) apply per agent; nested runs enforce their own caps.
- Streaming and memory hooks require no changes; events flow from the parent runtime.

