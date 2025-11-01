# AURA → goa-ai Migration Guide

This document provides an exhaustive, step-by-step plan to port AURA to the goa-ai framework while preserving existing functionality and improving design elegance. It emphasizes composition: goa-ai supplies the core agent runtime; AURA provides domain-specific planners, policies, subscribers, and clients. Each step includes validation so you can migrate incrementally and safely.

The plan assumes familiarity with AURA (~/src/aura) and goa-ai (this repo). Follow the repository guidelines in this repo (docs/plan.md, README, and the root style guidance).

Goals:
- Keep proven algorithms (planning loops, gating, tool selection) intact where they don’t benefit from rework.
- Compose AURA-specific behavior via policy engines and hook subscribers instead of forking core runtime.
- Favor copy/move over rewrite, keeping file history and minimizing risk.
- No backwards compatibility required: aim for the most elegant, idiomatic end-state.

Outcomes:
- Keep the service name orchestrator and keep it as a Goa service. It wires the goa-ai runtime with Temporal, Mongo, Pulse, and model integrations, and also exposes standard telemetry endpoints (at minimum: `/livez`, `/healthz`, `/metrics`) like other workers.
- Chat agent and ADA planner ported to `planner.Planner` interfaces; ADA exported as an agent-tool.
- Atlas Data (AD) defined as a toolset; tool execution runs via the goa-ai runtime’s ExecuteTool activities (no dedicated AD worker).
- Stream and session persistence integrated via hook subscribers (Pulse + Mongo stores).
- Approval logic expressed via `policy.Engine` with the same effective behavior.

See also:
- AURA API Types Migration Plan: `docs/aura_api_types_migration.md` (detailed, service-by-service type replacement guidance)

-------------------------------------------------------------------------------

## 0) Prepare, Read, and Inventory

1. Review AURA architecture
   - Read `~/src/aura/docs/ARCHITECTURE.md` end to end to refresh system boundaries, the durable workflow patterns, and the ADA fan‑out/fan‑in contract.

2. Inventory service designs (systematically)
   - For each service under `~/src/aura/services/*` open the `design/` package and skim the Goa designs to understand surface areas and payload types, especially:
     - `front`, `orchestrator`, `chat-agent`, `atlas-data-agent`, `atlas-data`, `session`, `todos`, `diagnostics-agent`, `remediation-*`.

3. Study inter-service interplay (focus set)
   - Trace a chat turn: front → orchestrator → Temporal → chat-agent activities → inference → ADA loop → AD tools → back to orchestrator → Pulse + session persistence.
   - Note where AURA publishes Pulse `ChatEvent`s and where session service persists `SessionEvent`s.

Validation
- Write down a short list of must-preserve contracts: 

-------------------------------------------------------------------------------

## Progress

- Completed
  - Agents DSL added to AURA:
    - chat-agent: Agent("chat") with caps and 90s time budget; Uses atlas.read.
    - atlas-data-agent: Agent("atlas_data_agent") with caps and 180s time budget.
    - remediation-planning-agent: Agent("remediation_planning_agent") with InterruptsAllowed(true) and 300s time budget.
  - Agents Toolsets replacing legacy Goa tools plugin:
    - atlas-data: Toolset("atlas.read") bound to atlas-data service methods.
    - todos: Toolset("todos.manage") bound to todos service methods.
    - Legacy ToolSet blocks commented out to avoid plugin dependency during migration.
  - Codegen (goa-ai) aligned (deterministic and fail‑fast):
    - Method‑backed tool executors now rely on generated transforms when shapes are compatible; imports and type refs use Goa NameScope (GoFullTypeRef) and UserTypeLocation.
    - Header imports are derived from UserTypeLocation (struct:pkg:path) and joined to the module gen/ root; no guessing.
    - No “fallback” branches: if a design invariant is violated, generation fails loudly.
    - Template fixes for goify and header placement remain.
  - Orchestrator host wiring:
    - goa-ai runtime (Temporal + Pulse) initialized;
    - Chat agent registered with a stub planner (no tool calls).
  - Todos integration with Chat:
    - Chat agent now Uses the `todos.manage` Toolset.
    - Regeneration produces `gen/chat_agent/agents/chat/todos_manage` and todos tools appear in `gen/chat_agent/agents/chat/tool_specs.Specs`.
    - Chat prompts derive the model tool list and brief from generated specs (no docs.json), so todos tools are advertised to the model.

- In Progress
  - Implement atlas.read executors + client and switch planner to call tools.
  - Register additional agents (ADA, remediation-planning) with planners as they migrate.
  - Add Mongo-backed memory/run stores and SSE bridge.
  - Apply Extend pattern for server payloads that implement tools: service method payload Types now Extend the shared tool Args and add server-only fields (e.g., session_id). Atlas Data has been migrated; todos exposes a clean Toolset and server payloads are already explicit — keep aligning other services as needed.
  - Strong contracts at run boundary: runtime requires SessionID (non‑empty) at Start; executors receive explicit ToolCallMeta (RunID, SessionID, TurnID, ToolCallID, ParentToolCallID). No context fishing for domain values.

- Next
  - Port chat/ADA planners to planner.Planner and wire policy/approvals.
  - Remove remaining legacy ToolSet plugin code paths.


-------------------------------------------------------------------------------
## Remaining Tasks (Tracker)

The tables below track open work across both repositories. Tasks are ordered by dependency and impact. Copy the exact commands and file paths to pick up easily.

Goa‑AI (this repo)

| ID | Area | Task | Description | Where | Verify | Status |
|----|------|------|-------------|-------|--------|--------|
| GA‑1 | Codegen | External MCP tool_specs: refer to existing types | External MCP toolsets reference existing types from their service packages rather than inlining. This is the chosen approach; types are imported and used directly. | N/A (design decision) | Integration uses existing types; no inlining needed. | Cancelled (use existing types) |
| GA‑2 | Tests | Add ToolUpdate stream mapping test | Cover hooks → stream mapping for ToolCallUpdatedEvent → stream.ToolUpdate with payload + correlation IDs. | agents/runtime/hooks/stream_subscriber_test.go | go test ./agents/runtime/hooks -run ToolUpdate. | Completed |
| GA‑3 | Codegen tests | Multi‑service MCP goldens | Strengthen goldens for multi‑service MCP stub + CLI generation (imports, aliases, commands). | features/mcp/codegen/golden_multi_service_test.go | go test ./features/mcp/codegen -run TestMultiService_GeneratesCLIAndStubs. | Completed |
| GA‑4 | Codegen tests | Transform helpers emission | Add generator tests asserting: transforms emitted for compatible shapes and omitted otherwise. | agents/codegen tests | go test ./agents/codegen -run Transforms. | Completed |
| GA‑5 | Integration | Always set a planner | Ensure example bootstrap registers a stub planner for each agent so tools/results are available during CI. | agents/codegen/templates/bootstrap_helper.go.tpl; example/complete/cmd/orchestrator/agents_planner_*.go | make itest with TEST_FILTER=MCPPrompts. | Completed |
| GA‑6 | Example | Optional MCP callers | In example bootstrap, wire optional real MCP callers (HTTP/SSE/Stdio) behind env flags; keep default stubs. | agents/codegen/templates/bootstrap_helper.go.tpl; example/complete/cmd/orchestrator/agents_bootstrap.go | make run-example; exercise MCP CLI. | Completed |
| GA‑7 | Docs | Update docs for streaming + executors | Add typed streaming example (ToolStart/Update/End) and document executor‑first + transforms. | docs/runtime.md, docs/plan.md, docs/dsl.md | N/A (docs). | Completed |
| GA‑8 | Dependencies | Upstream Goa recursion guard | Open PR and bump goa once merged (jsonrpc:id recursion guard). Remove local replace if present. | go.mod (replace), Goa PR link | make test in goa and goa‑ai. | Completed |

AURA (application repo)

| ID | Area | Task | Description | Where | Verify |
|----|------|------|-------------|-------|--------|
| AU‑1 | Todos | Expose todos to Chat (DONE) | Chat agent Uses todos.manage; prompts include todos tools from specs. | services/chat-agent/design/agents.go, services/chat-agent/prompts/builder.go | scripts/gen goa; build chat‑agent. |
| AU‑2 | Clean up | Remove legacy ToolSet/BRTool fallbacks | Replace remaining ToolSet/docs.json paths with generated tool_specs across agents (diagnostics, chat, todos). | services/*/tools, services/*/prompts | scripts/build; no imports from shared/tools or docs.json parsing. |
| AU‑3 | Inference | Build provider config from specs | Convert tool_specs.Specs → model provider config (Bedrock/OpenAI); no manual JSON schemas. | per‑agent tools/config_from_specs.go | Unit test config builders; run chat flow. |
| AU‑4 | AD | Align AD shapes in code | Update remaining AD code to match generated types (DeviceBrief, EquipmentStatus alias pointer, DependencyNode, etc.). | services/atlas-data/*, services/atlas-data/tools/defs/* | scripts/gen goa && scripts/build. |
| AU‑5 | AD | Implement remaining executors | Finish executors for time series, list apps, building shutdowns, temp summary, EHS, equipment/app status changes, list devices. | services/atlas-data-agent/tools/exec/* | Unit tests + compile. |
| AU‑6 | Schema | Public schema helpers from specs | Ensure publicschema reads payload/result directly from generated tool_specs (no docs.json fallbacks). | services/*/tools/publicschema.go | Unit/compile checks. |
| AU‑7 | Policy | Port approval engine | Re‑express approvals/caps using policy.Engine; emit labels; hook into runtime. | services/orchestrator policy package | Unit tests; chat turn with approvals. |
| AU‑8 | Persistence | Wire Mongo memory/run stores | Configure features/memory/run Mongo stores; verify event persistence matches streamed events. | services/orchestrator/cmd/orchestrator/main.go | Run a chat turn; inspect Mongo. |
| AU‑9 | Frontend | Consume new stream schema | Update UI to SSE stream event types (assistant_reply, tool_start, tool_update, tool_end, planner_thought). | Front service/UI | Manual run. |
| AU‑10 | Docs | Architecture + migration notes | Ensure ARCHITECTURE.md and migrate.md reflect inline ADA, tool_specs usage, and removal of docs.json/toolsets. | docs/ARCHITECTURE.md, docs/migrate.md | N/A (docs). |

Notes
- Execute AURA items in order: AU‑2 → AU‑3 → AU‑4/AU‑5 → AU‑7/AU‑8 → AU‑9.
- Goa‑AI items: GA‑1 cancelled (use existing types for external MCP toolsets). All other GA items completed.

## Action Items to Complete Migration

- Implement atlas.read executors (Chat, ADA)
  - Suggested paths: `~/src/aura/executors/chat_agent/chat_atlas_read.go`, `~/src/aura/executors/atlas_data_agent/atlas_read.go`.
  - Map tool args → AD method payloads; set session using `ToolCallMeta.SessionID` (or runtime labels). Map AD method results → tool result data only.
  - Cover at minimum: explain_control_logic, get_alarms, get_control_context, get_current_time, get_device_snapshot, get_equipment_status, get_time_series, get_topology, resolve_sources, list_active_alarms, list_created_alarms_in_window, list_apps, list_building_shutdowns, list_device_setting_changes, list_app_setting_changes, list_devices, list_equipment_mode_changes, list_app_status_changes, get_user_details, get_temperature_summary.
  - Tests: generated AD client mocks; table-driven field mapping checks.

- Replace stub planners with real planners
  - Chat planner: port to `planner.Planner` under `~/src/aura/services/chat-agent/planner` (PlanStart/PlanResume assemble messages, tool config from specs, streaming output).
  - ADA planner: port gating/selection loop to `~/src/aura/services/atlas-data-agent/planner` (produce ToolCalls only; no AD RPC inside planner).
  - Wire in orchestrator via generated registrations in `~/src/aura/services/orchestrator/cmd/orchestrator/main.go` and `agents_planners.go`.

- Register remediation-planning agent
  - Call `remediation_planning_agent.RegisterRemediationPlanningAgentAgent(...)` in orchestrator with a stub planner first; upgrade to real planner later.

- Convert Knowledge Agent to a goa-ai agent
  - Add Goa design: `Service("knowledge_agent")` with `Agent("knowledge_agent")` and `Toolset("knowledge.emit")` for emit tools (diagnostics summary, agent facts).
  - Generate code: `goa gen github.com/crossnokaye/aura/services/knowledge-agent/design` (then top-level `goa gen github.com/crossnokaye/aura/design`).
  - Register in orchestrator using generated `RegisterKnowledgeAgentAgent(...)` with a stub planner; wire real planner later.
  - Update any prompt/emit paths to consume generated `tool_specs` (no docs.json).

- Wire memory/run stores and SSE bridge
  - Configure goa‑ai `features/memory/mongo` and `features/run/mongo` in orchestrator runtime options.
  - Keep Pulse `RuntimeStreams`; add SSE bridge (or Pulse subscriber) for UI until `front` migrates.

- Introduce policy/approvals
  - Implement `policy.Engine` in `~/src/aura/services/orchestrator/policy`; port approval logic and caps from orchestrator approval code.
  - Register engine in runtime options; emit labels/metadata; add unit tests.

- Update front-end to new stream schema
  - Consume `assistant_reply`, `tool_start`, `tool_update`, `tool_end`, `planner_thought` event types in `front`.
  - Adjust progress UI using `ExpectedChildrenTotal` and parent/child IDs.

- Remove legacy tool/plugin fallbacks
  - Delete or fence `shared/tools/*`, `services/*/tools/defs/*`, and any `docs.json` consumers.
  - Ensure prompts/builders consume only generated `gen/.../agents/.../specs`.

- Build provider configs from specs
  - Add `config_from_specs.go` per agent to derive model provider config from `tool_specs.Specs` (no manual JSON schemas).
  - Unit tests for config builders.

  - Align AD shapes and complete remaining executors
  - Verify AD design types match shared tool types; `scripts/gen goa` and fix any mismatches.
  - Finish mappings for time series, list apps, building shutdowns, temperature summary, EHS, equipment/app status changes, list devices.

- Streaming and persistence verification
  - Run a chat turn end‑to‑end; verify Pulse event order and Mongo run/memory stores persistence.
  - Confirm model‑bound messages only include sanitized summaries + compact data.

- No dedicated AD worker queue: the goa‑ai runtime schedules and executes AD
  tool calls via ExecuteTool activities within the orchestrator’s workers.

- Clean up and finalize
  - Remove temporary build tags and stubs; update `docs/ARCHITECTURE.md` and this guide; ensure `scripts/gen` does not enable Goa docs plugin.

-------------------------------------------------------------------------------
tool schemas for AD, ADA child‑tracking behavior, chat streaming cadence, session persistence semantics (what’s critical vs. nice-to-have).

-------------------------------------------------------------------------------

## 1) Keep Orchestrator As A Goa Service (Runtime Host)

Objective: Replace the bespoke workflow logic while keeping the orchestrator as a Goa service that starts runs and exposes telemetry endpoints.

Steps
1. Add goa-ai to AURA repo `go.mod` temporary replace for local dev:
   - For local dev: `replace goa.design/goa-ai => ../goa-ai`

2. Orchestrator Goa service (in AURA repo)
   - Keep/define the Goa service `orchestrator` with a minimal HTTP API:
     - Start/Run endpoints for chat/diagnostics (mapping to `rt.StartRun` or `rt.Run`).
     - Telemetry endpoints: `/livez`, `/healthz`, `/metrics` (reuse existing handlers if present).
   - In `services/orchestrator/cmd/orchestrator/main.go` wire the runtime:
     - Builds a Temporal engine adapter (`agents/runtime/engine/temporal`).
     - Creates Mongo-backed memory and run stores (`features/memory/mongo`, `features/run/mongo`).
     - Builds Pulse runtime streams (`features/stream/pulse`).
     - Instantiates `runtime.New(runtime.Options{Engine, MemoryStore, RunStore, Stream, Policy: <placeholder>})`.
     - Registers agents (Chat, ADA) and their activities (Plan/Resume/ExecuteTool) via generated helpers (added in later steps).

3. Worker lifecycle
   - Use the Temporal engine defaults (auto-start workers on first run) or call `Worker()`/`Start()` explicitly if you prefer manual control.

Validation
- Build and run `orchestrator`; it should connect to Temporal, Mongo, and Redis and serve HTTP.
- `GET /livez` and `GET /healthz` return 200. `/metrics` scrapes without errors.

-------------------------------------------------------------------------------

## 2) Define Agents and Toolsets in Goa Designs

Objective: Move AURA Goa designs into goa-ai’s agent DSL and generate registries, codecs, and stubs.

Steps
1. Chat agent design
   - Create (or move) `services/chat/design/agents.go` using goa-ai agents DSL:
     - Define `Agent("chat", ...)` with a `Uses` block importing ADA’s exported toolset (added later) and any other toolsets (e.g., todos).
     - Set `RunPolicy` (caps, time budget) mirroring today’s orchestrator chat caps.

2. ADA (atlas-data-agent) design
   - Create `services/atlas-data-agent/design/agents.go`:
     - Define `Agent("atlas_data_agent", ...)`.
     - In `Exports`, publish ADA’s agent-tools that should be invocable by other agents (e.g., high-level data tasks that plan across AD calls). These become agent-as-tool entries.
     - In `Uses` (optional), include ADA-only internal toolsets (e.g., todos helpers) as needed.

3. AD (atlas-data) toolset design
   - In `services/atlas-data/design/tools.go`, define the deterministic tools ADA and chat rely on (list devices, key events, etc.). These mirror existing AD endpoints; transports remain in AD.

4. Generate code
   - `goa gen ...` to emit per-agent packages: runtime registries, tool specs/types/codecs, and registration helpers.
   - Do not edit generated files. Iterate on `design/*.go` until types match.

Validation
- Build: all generated packages compile. Tool specs for AD match current request/response schemas.
- Sanity: write a tiny unit test that loads generated tool specs and marshals a sample payload to ensure codec compatibility with the existing AD JSON.

Scaffold checklist (AURA repo)
- Create `design/shared/tools/atlas_data.go` with initial tool types: `ADGetAlarmsToolArgs`, `ADGetAlarmsToolReturn` (and others you need). Include precise descriptions, examples, and validations.
- Create `services/atlas-data/design/method_types.go` with `ADGetAlarmsPayload`/`ADGetAlarmsResult` using `CreateFrom`/`ConvertTo`/`ConvertFrom` to extend tool types (add `session_id`, etc.).
- Create/Update `services/atlas-data/design/agents_toolsets.go` to declare `Toolset("atlas.read")` with tools binding to AD methods via `BindTo("atlas_data.<method>")`. Import shared tool types via `. "<module>/design/shared/tools"`.
- Update `services/atlas-data/design/design.go` methods to use the new method types for `Payload`/`Result`.
- Create/Update `services/atlas-data-agent/design/agents.go` to declare the ADA agent and `Uses("atlas.read")`.
- Regenerate: `goa gen <module>/services/atlas-data/design` and `goa gen <module>/services/atlas-data-agent/design`.
- Verify build: go build the AD and ADA services; fix any type mismatches by adjusting the shared tool types.

-------------------------------------------------------------------------------

AD/ADA Migration Execution (current status)

Context: We’re migrating AD tool contracts to shared design types under `design/shared/tools`, deriving server method payloads to add session/auth fields, and updating the AtlasRead toolset to bind those tools to AD methods. ADA consumes the toolset via `Uses`.

What’s done (AURA repo):
- Shared tool args/returns in `design/shared/tools/atlas_data.go`:
  - Alarms: `ADGetAlarmsToolArgs`, `ADGetAlarmsToolReturn`
  - Time series: `ADGetTimeSeriesToolArgs`, `ADGetTimeSeriesToolReturn`
  - Device snapshot: `ADGetDeviceSnapshotToolArgs`, `ADGetDeviceSnapshotToolReturn`
  - Resolve sources: `ADResolveSourcesToolArgs`, `ADResolveSourcesToolReturn`
  - Topology: `ADGetTopologyToolArgs`, `ADGetTopologyToolReturn`
  - List active alarms: `ADListActiveAlarmsToolArgs`, `ADListActiveAlarmsToolReturn`
  - List created alarms in window: `ADListCreatedAlarmsInWindowToolArgs`, `ADListCreatedAlarmsInWindowToolReturn`
  - Control context: `ADGetControlContextToolArgs`, `ADGetControlContextToolReturn`
  - Current time: `ADGetCurrentTimeToolArgs`, `ADGetCurrentTimeToolReturn`
  - Explain control logic: `ADExplainControlLogicToolArgs`, `ADExplainControlLogicToolReturn`
  - Trace control dependencies: `ADTraceControlDependenciesToolArgs`, `ADTraceControlDependenciesToolReturn`
  - Equipment status: `ADGetEquipmentStatusToolArgs`, `ADGetEquipmentStatusToolReturn`
  - Device setting changes: `ADListDeviceSettingChangesToolArgs`, `ADListDeviceSettingChangesToolReturn`
  - App setting changes: `ADListAppSettingChangesToolArgs`, `ADListAppSettingChangesToolReturn`
  - List apps: `ADListAppsToolArgs`, `ADListAppsToolReturn` (with `AppSummary`)
  - Building shutdowns: `ADListBuildingShutdownsToolArgs`, `ADListBuildingShutdownsToolReturn`
  - Temperature summary: `ADGetTemperatureSummaryToolArgs`, `ADGetTemperatureSummaryToolReturn`
  - EHS context: `ADGetEhsContextToolArgs`, `ADGetEhsContextToolReturn`
  - Equipment mode changes: `ADListEquipmentModeChangesToolArgs`, `ADListEquipmentModeChangesToolReturn`
  - App status changes: `ADListAppStatusChangesToolArgs`, `ADListAppStatusChangesToolReturn`
  - List devices: `ADListDevicesToolArgs`, `ADListDevicesToolReturn` (with `DeviceBrief`)
  - User details: `ADGetUserDetailsToolArgs`, `ADGetUserDetailsToolReturn`

- Server method payloads (`services/atlas-data/design/method_types.go`):
  - Added `AD*Payload` types for each method above (e.g., `ADGetAlarmsPayload`, `ADGetTimeSeriesPayload`, …) with `session_id` and required fields; these are clean, server-owned types.

- Methods updated (`services/atlas-data/design/design.go`):
  - All corresponding AD methods now use the `AD*Payload` types; results remain the same and continue to include `atlas_calls` for interceptors.

- Toolset bindings (`services/atlas-data/design/toolsets.go`):
  - AtlasRead toolset now uses the shared tool args/returns for all tools listed above and binds them to AD methods via `BindTo`.

- ADA agent (`services/atlas-data-agent/design/agents.go`):
  - Uses the AtlasRead toolset: `Uses(func(){ Toolset(addesign.AtlasReadToolset) })`.

- Regeneration:
  - Ran `goa gen` for both AD and ADA designs; generation completed successfully.

Decisions and notes:
- Cross-package conversions: we used explicit server payload definitions rather than `CreateFrom/ConvertTo` across packages to avoid field-mapping issues. We can revisit and reintroduce conversions once we align cross-package conversion semantics.
- Interceptors: AD results keep `atlas_calls` fields to preserve existing `RecordAtlasCalls`/`CaptureAtlasCalls` behavior.
- Style: Shared tool types contain only agent-facing fields; server-exclusive fields (e.g., `session_id`) live in server payloads.

How to regenerate (AURA):
- `goa gen github.com/crossnokaye/aura/services/atlas-data/design`
- `goa gen github.com/crossnokaye/aura/services/atlas-data-agent/design`

Next steps:
  - Add remaining executors (time series, list apps, building shutdowns, temperature summary, EHS, equipment/app status changes, list devices) following the same pattern.
- Extend descriptions/examples: enrich shared tool type field descriptions and examples to match production docs precisely.
- Consider reintroducing `CreateFrom/ConvertTo` once cross-package conversion semantics are validated and stable.

-------------------------------------------------------------------------------

Disconnect Orchestrator from Legacy Tool Plugins (Done)

- Replaced legacy atlasdefs/shared/tools usage in orchestrator with generated ADA tool_specs (adatoolspec):
  - Converter: keep `ToolCall.Params` unset and preserve `InputJSON`. Do not decode in the Temporal converter; decoding to typed payloads happens at execution time using the generated tool_specs codecs.
  - Display hints: use a small local helper for rendering hints; do not import plugin-era helpers.
  - Policy helpers: copy the masked-tools policy into orchestrator to avoid plugin imports while preserving behavior.

- Safety: Remove plugin‑era packages from the active build instead of fencing. Delete the legacy code paths that are superseded by goa‑ai tool_specs and codecs to avoid accidental linkage:
  - Delete `services/orchestrator/tools/defs/*` once the generated tool_specs and executors are wired.
  - Delete `services/atlas-data-agent/tools/defs/*` once ADA executors and codecs are wired.
  - Delete helpers in `shared/tools/*` that implement legacy schema extraction/validation (schema_helpers.go, validation.go, examples.go) and scrub call sites. Replace with generated tool_specs + JSON codec flows.
  - Drop the Goa docs plugin and any `docs.json` introspection. Services must not parse documentation at runtime; rely exclusively on generated tool specs for schema and encoding.

How to regenerate and build (AURA):
- `cd ~/src/aura`
- `scripts/gen goa`
- Targeted build while unrelated services migrate:
  - `go build ./gen/... ./services/orchestrator/...`
 - Ensure generation scripts do not enable the Goa docs plugin; no `docs.json` is produced or consumed.

Notes
- Tool call names are treated as strong, canonical IDs (e.g., `atlas_data_agent.atlas.read.get_time_series`). The converter tolerates bare suffixes during migration by suffix-matching against `Specs`.
- The Extend pattern keeps tool contracts clean and server payloads explicit. Keep using it for any service that implements tools (e.g., todos) when binding toolsets with `BindTo`.
 - Deterministic type refs/imports: If a tool binds to a service method whose types are generated under `gen/types`, the generator imports `gen/types` explicitly and references `*types.Foo` in signatures. If the method types are local to the service package, it references `*<servicepkg>.Foo`.

Schemas and Codecs via tool_specs

- Source of truth: use `gen/<svc>/agents/<agent>/<toolset>/tool_specs.Specs` for schemas and JSON codecs.
- Example (lookup by name; typed and untyped codecs):

```go
import (
    adaread "github.com/yourorg/yourrepo/gen/atlas_data_agent/agents/atlas_data_agent/atlas_read/tool_specs"
    "goa.design/goa-ai/runtime/agent/tools"
)

func find(name string) (tools.ToolSpec, bool) {
    for _, s := range adaread.Specs {
        if s.Name == name {
            return s, true
        }
    }
    return tools.ToolSpec{}, false
}

// Use schemas/codecs
spec, _ := find("atlas_data_agent.atlas.read.get_alarms")
payloadSchema := spec.Payload.Schema
resultSchema := spec.Result.Schema

// Untyped codec
b, _ := spec.Payload.Codec.ToJSON(map[string]any{"site_id": "acme"})

// Typed codec (from generated package if you have the concrete type)
// b, _ := adaread.MarshalAtlasDataAgentAtlasReadGetAlarmsPayload(v)
```

ADA consumption of tool_specs (no wrappers)

- Do not add helper wrappers like `tools/specs.go`. Consume the generated
  tool specs directly:
  - Iterate `gen/<svc>/agents/<agent>/<toolset>/tool_specs.Specs` for names,
    descriptions, and payload/result schemas.
  - If the inference engine requires `BRTool`, convert at the provider boundary
    only; keep the rest of ADA code working with `tool_specs.Specs` directly.
- In ADA, building the model tool list should iterate `tool_specs.Specs` without
  docs.json or shared/tools fallbacks. This is already implemented via
  `buildADAToolSet` using `adaspec.Specs`.

Testing guidelines (ADA)

- Use generated mocks for AD clients (e.g.,
  `services/atlas-data-agent/clients/atlasdata/mocks.Client`) when testing
  executor paths. Do not create custom core glue.
- Validate that:
  - Executors set `session_id` in method payloads from `ToolCallMeta.SessionID`
    when your server requires it and copy tool‑arg fields appropriately (use
    transforms if compatible).
  - Tool results returned to planners are data‑only (no server‑only fields).
  - The runtime decode/encode path uses the generated `tool_specs` codecs.

Method vs. Tool Results (and the Execution Envelope)

- Method result (service API) is a pure domain type and is a superset of what tools expose. It must not contain server-only fields (evidence, atlas calls, duration, retry/error metadata, summaries). Keep service methods domain-first.
- Tool result (in tool_specs) is data-only. Generated tool result types must include only the domain data needed by planners/LLMs. Do not place server-only fields in tool_specs.
- Execution envelope (server-only) represents the outcome of a tool execution for storage/observability: code, summary, data (JSON), evidence refs, atlas calls, duration, error, facts. In AURA this is ADAToolResult. The envelope is built at the execution layer (orchestrator runtime), not by service methods, and is never sent to the model.
- Sanitization for model prompts: prompts must only include a minimal, sanitized view derived from the envelope: summary + compact data. Never include evidence/calls/duration in model messages.

Executor and runtime guidance
- goa‑ai service toolsets are registered by application code using executors. Executors map tool args → method payloads and method results → tool result data (only). Do not add server‑only fields to the planner path.
- The execution envelope is composed outside the planner path (e.g., in ADA activities). That code attaches server‑only metadata and persists/streams the full envelope via hooks/subscribers.
- ADA loop accumulates envelopes (server‑side) and decides what to surface upstream; it may hide some results or summarize others before returning to the parent agent. Model‑facing content always uses the sanitized form.

Migration actions (AURA)
- Review AD tool_specs result types and strip any envelope fields. Tool results must be data-only.
- Ensure ADAToolResult (execution envelope) is constructed in the runtime’s ExecuteTool activity path (the execution layer), not in service methods. Use `FormatResultSummary` to populate the summary and attach evidence/calls/duration exactly as before.
- Confirm inference-engine only injects sanitized result content in Bedrock messages. Use `sanitizeToolResultForModel` and `formatToolResultPayload` consistently for both start and resume paths.
- Clarify naming in code/docs: ADAToolResult is an execution envelope (server-only), not a tool result. Tools expose the data subset only.

Verification
- For a representative set of AD tools, compare:
  - Method result JSON (domain) — unchanged or improved clarity.
  - Tool result JSON (data-only) — matches the subset expected by planners.
  - Execution envelope JSON — matches “backup” (code, summary, evidence, calls, duration, error, facts, data) for the same inputs.
- Validate that model-bound messages never include evidence/calls/duration and that summaries match “backup”.

Summaries

- Prefer runtime data over schema text:
  - Streaming: use `planner.ConsumeStream` to aggregate a `StreamSummary` (final text, tool calls, usage) and present `summary.Text`.
  - Typed: derive concise summaries from concrete result fields in executors/service code (counts, key labels). Avoid parsing JSON Schemas to build summaries.

-------------------------------------------------------------------------------

Orchestrator Wiring (runtime + agents)

- Runtime host (services/orchestrator/cmd/orchestrator/main.go):
  - Initializes goa-ai runtime (Temporal + Pulse) and registers the Chat agent.
  - Registers Atlas Data Agent (ADA) and wires the atlas.read toolset client:
    - Builds a raw `gen/atlas_data` client via `gen/grpc/atlas_data/client.NewClient` endpoints and `gen/atlas_data.NewClient(...)`.
    - Passes it into the generated ADA config: `adaagent.AtlasDataAgentAgentConfig{ AtlasReadCfg: adaread.AtlasReadConfig{ Client: adClient } }`.
    - Implements an initial set of executors (get_alarms, get_device_snapshot, get_control_context, get_current_time, get_topology, resolve_sources, list_active_alarms, list_created_alarms_in_window, get_user_details). These map tool args to method payloads (including `session_id`) and map method results to tool returns. Add remaining executors incrementally as you enable tools.
    - When mapping, return pointer values for method payloads and respect value/pointer semantics in generated types. For results, dereference optional fields (e.g., `DeviceAlias`) before assigning to value fields in tool results.
    - `session_id` is currently set via a helper (`sessionIDFromContext`) returning a stable dev value. In production, derive it from runtime context (labels/policy) so calls route correctly.
    - Pass explicit `ToolCallMeta` (RunID, SessionID, TurnID, ToolCallID, ParentToolCallID) into your mapping logic; do not extract these from `context.Context`.

- Why this shape:
  - The generated toolset registry references the exact Goa‑generated type names for method payloads/results (e.g., `ADGetAlarmsPayload`) via transforms, avoiding fragile naming assumptions.
  - This keeps executor code explicit, type‑safe, and localized in orchestrator configuration.

-------------------------------------------------------------------------------

Emit Tools as First‑Class Types (Knowledge + Remediation)

Problem
- Emit tools (e.g., `emit_diagnostics_summary`, `emit_agent_facts`, `emit_plan_result`) were historically treated as special cases via ad hoc JSON schemas.

Approach
- Model emit tools like any other tool:
  - Add explicit Goa Types for payloads/results with rich descriptions, examples, and validations.
  - Declare them in dedicated toolsets exported by each agent.
  - Let codegen produce JSON schemas and codecs under `gen/<service>/agents/<agent>/tool_specs`.

Changes (AURA)
- knowledge-agent:
  - Types: `EmitDiagnosticsSummaryPayload/Result`, `AgentFact`, `EmitAgentFactsPayload/Result`.
  - Toolset: `Toolset("knowledge.emit")` with `emit_diagnostics_summary` and `emit_agent_facts`.
- remediation-planning-agent:
  - Types: `RollbackAction`, `PlanAction`, `EmitPlanResultPayload/Result`.
  - Toolset: `Toolset("remediation.emit")` with `emit_plan_result`.

Builders
- Aggregate ADA tool_specs with emit tool_specs to form the Bedrock tool list for each agent. Avoid manual JSON; use generated schemas/codecs.

Regenerate and Build
- `goa gen github.com/crossnokaye/aura/services/knowledge-agent/design`
- `goa gen github.com/crossnokaye/aura/services/remediation-planning-agent/design`
- Then `goa gen github.com/crossnokaye/aura/design` and rebuild impacted services.

-------------------------------------------------------------------------------

Deterministic Type Refs and Conversions (goa-ai generator rules)

- Always use Goa NameScope (GoFullTypeRef/Name) and UserTypeLocation for type references; no string/asterisk surgery or guessing imports.
- Fail fast on missing qualifications; do not introduce fallback branches.
- Server payloads for method-backed tools are explicit `AD*Payload` types (with `session_id`). Cross-package `CreateFrom/ConvertTo` may be reintroduced once stable.


Executor stubs (pattern):
- Executor mapping (tool args -> method payload):
  - Copy fields from tool args; set `SessionID` from ToolCallMeta if needed. Use transforms when compatible.
- Executor mapping (method result -> tool return):
  - Copy fields or use transforms; preserve provenance fields where appropriate (outside of planner‑facing results).

-------------------------------------------------------------------------------

### AD/ADA Type Conventions (Tool vs. Method)

Goal: Stop “scrubbing” fields manually when routing between ADA and AD. Define agent‑facing tool shapes once, then derive server method shapes from them using Goa conversions.

Naming convention
- Tool types (agent‑facing, no server fields):
  - `ADGetAlarmsToolArgs` (payload)
  - `ADGetAlarmsToolReturn` (result)
- Method types (server handlers, extend tool types):
  - `ADGetAlarmsPayload` (payload)
  - `ADGetAlarmsResult` (result)

Pattern
- Define tool types once (no session/auth/diagnostics). Place them in a shared design package importable by both AD and ADA (convention: `design/shared`).
- Define AD method types with `CreateFrom(<ToolType>)` then add server‑only fields (e.g., `session_id`, `request_id`, diagnostics). Add `ConvertTo(<ToolType>)` for payloads and `ConvertFrom(<ToolType>)` for results so Goa generates conversion helpers.
- Define the AD Toolset using the tool types and bind tools to AD methods via `BindTo("atlas_data.<method>")`.
- In ADA, declare the Agent and `Uses("atlas.read")`; ADA never re‑declares tool shapes.

Goa DSL example (illustrative)
```go
// design/shared/tools/atlas_data.go
Type("ADGetAlarmsToolArgs", func() {
    Description("Arguments to request alarms from Atlas Data. No server-only fields.")
    Field(1, "site_id", String, "Site identifier")
    Field(2, "after", String, "RFC3339 start time window", func() { Format(FormatDateTime) })
    Field(3, "severity", ArrayOf(String), "Optional severities filter")
    Required("site_id")
    Example(Val{ "site_id": "acme-west", "after": "2024-10-01T00:00:00Z" })
})

Type("ADGetAlarmsToolReturn", func() {
    Description("Alarms list returned by Atlas Data.")
    Field(1, "alarms", ArrayOf(String), "Alarm IDs or summaries")
    Required("alarms")
    Example(Val{ "alarms": Val{ "A-1", "A-2" } })
})

// services/atlas-data/design/method_types.go
Type("ADGetAlarmsPayload", func() {
    Description("Server payload for get_alarms; extends tool args with session/auth.")
    CreateFrom(ADGetAlarmsToolArgs)
    Field(90, "session_id", String, "Session for tenancy/auth")
    Required("session_id")
    ConvertTo(ADGetAlarmsToolArgs)
})

Type("ADGetAlarmsResult", func() {
    Description("Server result for get_alarms; extends tool return with diagnostics.")
    CreateFrom(ADGetAlarmsToolReturn)
    Field(90, "server_elapsed_ms", Int, "Server processing time in ms")
    ConvertFrom(ADGetAlarmsToolReturn)
})

// services/atlas-data/design/agents_toolsets.go
// import shared tool types for Payload/Result:
//   . "github.com/yourorg/yourrepo/design/shared/tools"
Toolset("atlas.read", func() {
    Description("Atlas Data read-only tools")
    Tool("get_alarms", func() {
        Description("List alarms matching filters")
        Payload(ADGetAlarmsToolArgs)
        Result(ADGetAlarmsToolReturn)
        BindTo("atlas_data.get_alarms")
    })
    // ... other tools
})

// services/atlas-data/design/design.go
Method("get_alarms", func() {
    Payload(ADGetAlarmsPayload)
    Result(ADGetAlarmsResult)
    // HTTP routes / errors as needed
})

// services/atlas-data-agent/design/agents.go (ADA)
Service("atlas_data_agent", func() {
    Agent("ada", func() {
        Uses("atlas.read")
        // Capabilities / MaxExecutionDuration / etc.
    })
})
```

Rules and notes
- Tool types are the public contract; they must include precise descriptions, examples, and validations. Do not include server‑only fields.
- Method types derive from tool types and add server‑only fields. Rely on generated conversions; do not manually “scrub” fields in handlers or planners.
- Non‑primitive, non‑slice, non‑map fields should be pointers per Goa’s rule.
- Keep toolset ownership with AD (the producer). ADA only consumes toolsets via `Uses`.
- After edits, run `goa gen` for AD and ADA; the generated transforms will be used by executors and method handlers automatically.

5. Schema and description review (mandatory)
- Copy exact tool descriptions from AURA into the DSL:
  - Every `Tool` and every `Field` must have a precise `Description("...")`/4th-argument description string matching production wording.
  - Provide `Example(...)` values and validation (`Minimum/Maximum`, `MinLength/MaxLength`, `Enum`, `Format`) where applicable.
- Preserve exact prompts:
  - System prompts and per-tool prompt snippets must be copied verbatim (no paraphrasing). When using templates, ensure `missingkey=error` and keep content identical to existing strings.

Validation
- Diff descriptions/prompts against the current AURA sources; they must match exactly.
- Regenerate and ensure descriptions appear in generated `tool_spec.go` and types.

-------------------------------------------------------------------------------

## 3) Port Planners (Chat, ADA) to `planner.Planner`

Objective: Move the core planning algorithms without changing their logic, wiring them to goa-ai model and memory interfaces.

Steps
1. Model client
   - Replace calls to AURA’s inference-engine RPC with goa-ai Bedrock/OpenAI clients (`features/model/bedrock` or `features/model/openai`).
   - Build the client once in the orchestrator and `rt.RegisterModel("bedrock", client)` if needed, or inject directly into planner constructors.

2. Chat planner
   - Copy the chat iteration logic from `services/orchestrator/workflows/chat_*.go` and the `chat-agent` service into a new `planner` package (e.g., `services/chat/planner`).
   - Implement `PlanStart` and `PlanResume`:
     - Compose messages/system prompt, tool configuration (allowlist), and stream model output if desired.
     - Return `ToolCalls` (batch) or `FinalResponse` matching current behavior.

3. ADA planner
   - Copy ADA loop logic from `services/atlas-data-agent` (e.g., `resume_state.go`, tool gating/selection, retry hint logic, emit tool dedup) into `services/atlas-data-agent/planner`.
   - Implement `PlanStart`/`PlanResume` to request AD tool calls (no AD RPC inside the planner). Keep the method‑specific allowlists and per‑turn gating intact.
   - When ADA is invoked as a tool (agent-as-tool), rely on `ExecuteAgentInline`; do not spawn separate workflows.

4. Wire planners
   - In orchestrator bootstrap, construct planner instances and pass them in the generated `AgentRegistration` for each agent.

Validation
- Unit tests: port existing planner unit tests (if any) and add table-driven tests for critical prompts/allowlists and retry hint classification.
- Dry run: call `rt.RunAgent("chat", messages...)` with tools disabled and verify a clean `FinalResponse` path.

-------------------------------------------------------------------------------

## 4) Execute Tools via Temporal Activities (AD) and Agent-as-Tool (ADA)

Objective: Route tool execution uniformly through the runtime. Deterministic tools execute as activities; exported ADA methods run inline as agent-tools.

Steps
1. AD toolset implementation (must go through Temporal)
   - Create an AD client wrapper in AURA (reuse existing client code).
   - Implement a toolset registration `ToolsetRegistration{ Name: "atlas.read", TaskQueue: "ad", Execute: func(ctx, call) ... }` that dispatches to AD RPCs and returns `planner.ToolResult`.
   - Register this toolset with the runtime; generated specs ensure JSON payload/result fidelity.
   - Ensure tool execution always happens as Temporal activities:
     - The workflow always schedules `ExecuteToolActivity`.
     - The runtime picks the activity queue from the toolset (`TaskQueue: "ad"`).
     - Register the generated `ExecuteToolActivity` and the AD toolset with the orchestrator’s workers; these perform the AD RPCs. ADA and chat planners never call AD directly.

2. ADA agent-as-tool (inline via ExecuteAgentInline)
   - Use generated `NewAgentToolsetRegistration(rt, cfg)` in ADA’s package to expose exported methods as tools.
   - Provide per-tool message templates or texts for agent-tools so ADA planners receive the correct user intent.
   - When these tools run, the runtime calls `ExecuteAgentInline` and tracks parent/child IDs and expected child totals.

3. Zero conditional workflow code
   - All dispatch happens in toolset.Execute; workflow always schedules `ExecuteToolActivity` for tools. AD calls are handled by the goa-ai runtime’s ExecuteTool activity; ADA exported tools run inline; planners do not make network calls to AD directly.

Validation
- Unit test: invoke AD toolset `Execute` for a few tools with canned payloads; assert result envelopes and retry hint propagation.
- Integration: run a chat turn that triggers at least one AD tool and one ADA agent-tool; assert that child updates (`ToolCallUpdatedEvent`) and final tool results arrive.

-------------------------------------------------------------------------------

## 5) Streaming and Session Persistence

Objective: Stream run events and persist timelines using goa-ai hooks and AURA subscribers.

Steps
1. Streaming (Pulse)
   - Construct `features/stream/pulse.RuntimeStreams` with a Pulse client and pass `Sink()` to runtime options.
   - Register an additional AURA subscriber on `rt.Bus` to publish any AURA-specific envelopes your UI needs (if you decide not to migrate UI yet). Otherwise, update `front` to consume the new stream schema directly.

2. Memory and run stores (Mongo)
   - Configure `features/memory/mongo` and `features/run/mongo` with the same MongoDB.
   - The runtime’s default memory subscriber persists tool calls, tool results, assistant messages, and planner notes.
   - If your UI relies on additional event classes, attach another subscriber that appends richer records into your existing `session` collections or a new collection specialized for agents.

3. Turn and child tracking
   - Confirm that `ToolCallScheduledEvent` and `ToolResultReceivedEvent` now carry `ToolCallID` and `ParentToolCallID`. UIs can compute progress as “completed child results / ExpectedChildrenTotal”.

Validation
- End-to-end: start a chat turn, subscribe to `run/<run-id>` via Pulse subscriber or SSE bridge, and verify ordered events (planner → tool_start → tool_end → final assistant message).
- Persistence: query the Mongo run/memory stores and confirm appended entries match the streamed events.

-------------------------------------------------------------------------------

## 6) Policy and Approvals

Objective: Reexpress approval/override logic using `policy.Engine` so runtime caps and allowlists are applied per turn.

Steps
1. Implement a `policy.Engine` in AURA
   - Port the essential logic from `services/orchestrator/approval` into a single engine that:
     - Reads retry hints and enforces clarifying/auto-retry modes where appropriate.
     - Adjusts `CapsState` (max tool calls, consecutive failures).
     - Optionally consults approval rules to disable/enable tools.

2. Register the engine in runtime options and emit audit labels via `Decision.Labels`/`Metadata`.

Validation
- Unit tests: table-driven scenarios that feed retry hints and requested tools; assert allowlist and cap changes.
- End-to-end: trigger a flow that requires an approval decision and confirm tools are disabled/enabled accordingly; verify `PolicyDecisionEvent` on the bus.

-------------------------------------------------------------------------------

## 7) Front-End Integration

Objective: Update `front` to the new event schema and start/run API.

Steps
1. Orchestrator Start/Run API (keep orchestrator as a Goa service)
   - Keep/adjust the orchestrator HTTP endpoints to call `rt.StartRun` (or `rt.Run`) for the chat agent.
   - Return the `RunID` (and optionally `TurnID`) so the UI can subscribe to `run/<run-id>` streams.
   - Keep telemetry endpoints `/livez`, `/healthz`, `/metrics` in the same service.

2. SSE
   - Implement a small bridge that subscribes to Pulse `run/<run-id>` and streams the events to the browser.
   - Update UI to consume `assistant_reply`, `tool_start`, `tool_end`, and `planner_thought` event types.

Validation
- Browser: start a chat; verify real-time updates and final messages match existing behavior.
- Latency: check no duplicate events and progress bars update as children are discovered and complete.

-------------------------------------------------------------------------------

## 8) Deployment and Operations

Steps
1. Environment
   - Configure Temporal namespace and default task queues.
   - Provide Redis/Pulse connection, Mongo URIs, Bedrock/OpenAI credentials via flags/env.

2. Workers
- Ensure worker registration covers the task queues used by your runtime; queue separation is optional and service-agnostic.

3. Observability
   - Enable OTEL interceptors in the Temporal engine adapter (already wired by default).
   - Use hook subscribers for structured logs and metrics.

Validation
- Smoke: run a synthetic scenario (no AD) and verify end-to-end health.
- Load: parallel turns with multiple tools to confirm futures and collection order are stable.

-------------------------------------------------------------------------------

## 9) Migration Mechanics (Copy/Move Guidance)

Prefer moving/renaming files to preserve history. Examples:
- Move ADA prompting and gating logic from `services/atlas-data-agent/*.go` into `services/atlas-data-agent/planner` without algorithm changes.
- Move AD client wrappers into a dedicated `clients/atlasdata` package; reuse exactly where possible.
- Copy system prompts and templates verbatim; only translate scaffolding to goa-ai constructs (messages, templates, tool specs).
- Copy tool descriptions verbatim into the DSL (tool-level and field-level descriptions). Use Examples and validations for schema clarity.
- Delete obsolete orchestrator workflow logic once planners and runtime wiring pass integration tests.

-------------------------------------------------------------------------------

## 10) Verification Checklist (End-State)

- Chat/tool parity
  - ADA dynamically discovers child tools; parent’s ExpectedChildrenTotal updates across iterations.
  - UI reconstructs parent/child tree and progress from stream events.
  - Turn sequencing is monotonic and deterministic.
  - Parallel tool execution across task queues works.

- Policy and caps
  - Max tool calls and consecutive failure caps enforced.
  - Approval logic produces expected allowlists and labels.

- Persistence and replay
  - Pulse event shape is consistent and sufficient for UI.
  - Run/memory stores contain the canonical timeline; large payloads handled.
  - Temporal replay is deterministic.

-------------------------------------------------------------------------------

## Appendix A: Step-by-Step Validation Scripts

- Unit test all packages in AURA after each phase:
  - `go test -race -vet=off ./...`

- Minimal end-to-end chat turn:
  1) Start Redis, Mongo, Temporal.
  2) Run orchestrator binary.
  3) Call `POST /api/start_chat` (or equivalent) to invoke `rt.StartRun`.
  4) Subscribe to Pulse `run/<run-id>`; expect: planner_thought → tool_start → tool_end → assistant_reply.

- Tool execution sanity (no planner):
  - Create a small CLI that calls AD toolset.Execute with a known payload and prints result JSON.

- Policy sanity:
  - Inject a retry hint and assert policy disables a tool next turn; verify `policy_decision` hook event.

-------------------------------------------------------------------------------

## Appendix B: Design Notes and Tradeoffs

- We avoid reproducing AURA’s bespoke orchestrator: the runtime loop, futures, child tracking, turn sequencing, and events are in goa-ai.
- We keep ADA’s selection/gating logic intact; the value lies in stable behavior, not in rephrasing algorithms.
- Streaming is generic; attach your own subscriber to emit legacy envelopes if needed. With no backwards‑compat requirement, prefer migrating `front` to the new schema.
- All domain coupling (knowledge, approvals, todos) lives in AURA feature modules implementing the runtime interfaces.
