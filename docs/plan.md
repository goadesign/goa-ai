# Plan and Architecture Notes

This document captures the evolving plan for the agents runtime, codegen, and example wiring. It complements `docs/runtime.md` (reference) and `docs/dsl.md` (design authoring).

## Recent Updates

- Executor‑first service toolsets
  - Generated service toolsets expose a single constructor `New<Agent><Toolset>ToolsetRegistration(exec runtime.ToolCallExecutor)`; applications register executors explicitly.
  - No adapters in core. Mapping is handled inside executors.
- Per‑tool transforms (when compatible)
  - Codegen emits `ToMethodPayload_<Tool>` and `ToToolReturn_<Tool>` under `specs/<toolset>/transforms.go` using Goa’s `GoTransform`.
  - Executors can use transforms to avoid boilerplate. If no transform is emitted, write an explicit mapper.
- Strong contracts, no heuristics
  - No best‑effort field fishing; explicit mappings only. Fail fast on contract violations.
- Streaming hooks and bridge
  - Typed events (`ToolStart`, `ToolUpdate`, `ToolEnd`, `PlannerThought`, `AssistantReply`) and a bridge from hooks → stream sink.
- Documentation cleanup
  - Updated runtime docs to reflect executor‑first model and transforms; removed adapter‑bypass section.
 - Example executors and bootstrap (goa example phase)
   - Emits `internal/agents/<agent>/toolsets/<toolset>/execute.go` with typed decode and transform placeholders.
   - `internal/agents/bootstrap/bootstrap.go` registers agents and method‑backed toolsets using `runtime.ToolCallExecutorFunc`.
 - Codegen tests for transforms and scaffolding
   - Added positive/negative transform emission tests and golden tests for internal bootstrap and executor stub files.
 - Streaming SSE E2E coverage
   - Extended example SSE test to assert tool_call, tool_result, and planner thought messages in addition to the final assistant reply.

## Authoring Guidance

- Prefer shared Goa `Type("Name", ...)` for payload/result shapes reused by both service methods and tools.
- Keep non‑primitive shapes as pointers in user types to satisfy Goa’s validation/codecs.
- Do not edit generated code; adjust the DSL or executors instead.

## Runtime Integration

- Register agents via generated `Register<Agent>` helpers which wire workflows, activities, toolsets, codecs, and policies.
- To stream events to clients, pass a `stream.Sink` in `runtime.Options.Stream`; the runtime auto‑registers the stream subscriber.
- Use `runtime.SetDefault(rt)` during bootstrap so generated workflow/activities route to your runtime instance.

## Next Steps

- Tool update streaming semantics
  - Decide whether to expose a dedicated `tool_update` chunk in the orchestrator streaming schema or map updates to `Status` consistently; extend SSE test accordingly once the schema is settled.
- Transform helper robustness
  - Add stricter negative tests for deeply incompatible shapes and nested objects; ensure no helpers are emitted in those cases.
- Developer DX
  - Add flags or minimal config in the example to point MCP callers at a local endpoint (without relying on env), and document it briefly in `AGENTS_QUICKSTART.md`.

## Testing & CI

- Run `make lint` to enforce style; avoid commented‑out code and keep files under 2k LOC.
- Run `make test` to execute unit + integration tests.
- For codegen goldens, run `go test -u ./agents/codegen/tests` when changing templates.
