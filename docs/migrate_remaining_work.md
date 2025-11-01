# Remaining Work from migrate.md

## Goa-AI Repository (this repo)

### Cancelled Tasks

**GA-1: External MCP tool_specs: refer to existing types** (Status: Cancelled)
- **Area**: Codegen
- **Description**: External MCP toolsets reference existing types from their service packages rather than inlining. This is the chosen approach; types are imported and used directly.
- **Location**: N/A (design decision)
- **Verification**: Integration uses existing types; no inlining needed.
- **Note**: Decision made to use existing types instead of inlining all user types in tool_specs/types.go

---

## AURA Repository (application repo)

### Remaining Tasks (AU-2 through AU-10)

Reference:
- For concrete type replacements across services (orchestrator, chat-agent, atlas-data-agent, inference-engine, front, session), see `docs/aura_api_types_migration.md`.

**AU-2: Remove legacy ToolSet/BRTool fallbacks** (Status: Completed)
- **Area**: Clean up
- **Description**: Replace remaining ToolSet/docs.json paths with generated tool_specs across agents (diagnostics, chat, todos)
- **Location**: `services/*/tools`, `services/*/prompts`
- **Verification**: `scripts/build`; no imports from shared/tools or docs.json parsing
- **Priority**: High (dependency order: execute first)
- **Task Breakdown**: See `docs/au2_todos.md` for detailed step-by-step tasks
  - Progress:
    - ✅ `FormatCallHint` moved to `services/atlas-data/hints`, imports updated
    - ✅ Todos guidance moved to `services/todos/prompts`, imports updated
    - ✅ Deleted legacy `services/atlas-data/tools/defs/*` files gated by `//go:build legacytools`
    - ✅ Verified no active `docs.json` parsing remains (only comments/generator artifact)

**AU-3: Build provider config from specs** (Status: Partially Complete ~60%)
- **Area**: Inference
- **Description**: Convert tool_specs.Specs → model provider config (Bedrock/OpenAI); no manual JSON schemas
- **Location**: `per-agent tools/config_from_specs.go`
- **Verification**: Unit test config builders; run chat flow
- **Priority**: High (dependency order: execute second)
- **Status Details**: See `docs/au3_status.md` for full assessment
  - ✅ Using `tool_specs.Specs` as source (no manual schemas)
  - ❌ Still converting to `BRTool` instead of `model.ToolDefinition`
  - ❌ Missing centralized `config_from_specs.go` helpers
  - ❌ Not using goa-ai `model.Client` abstraction yet

**AU-4: Align AD shapes in code** (Status: Pending)
- **Area**: AD
- **Description**: Update remaining AD code to match generated types (DeviceBrief, EquipmentStatus alias pointer, DependencyNode, etc.)
- **Location**: `services/atlas-data/*`, `services/atlas-data/tools/defs/*`
- **Verification**: `scripts/gen goa && scripts/build`
- **Priority**: High (can execute in parallel with AU-5)

**AU-5: Implement remaining executors** (Status: Pending)
- **Area**: AD
- **Description**: Finish executors for: time series, list apps, building shutdowns, temp summary, EHS, equipment/app status changes, list devices
- **Location**: `services/atlas-data-agent/tools/exec/*`
- **Verification**: Unit tests + compile
- **Priority**: High (can execute in parallel with AU-4)

**AU-6: Public schema helpers from specs** (Status: Pending)
- **Area**: Schema
- **Description**: Ensure publicschema reads payload/result directly from generated tool_specs (no docs.json fallbacks)
- **Location**: `services/*/tools/publicschema.go`
- **Verification**: Unit/compile checks
- **Priority**: Medium

**AU-7: Port approval engine** (Status: Pending)
- **Area**: Policy
- **Description**: Re-express approvals/caps using policy.Engine; emit labels; hook into runtime
- **Location**: `services/orchestrator policy package`
- **Verification**: Unit tests; chat turn with approvals
- **Priority**: High (dependency order: execute after AU-4/AU-5)

**AU-8: Wire Mongo memory/run stores** (Status: Pending)
- **Area**: Persistence
- **Description**: Configure features/memory/run Mongo stores; verify event persistence matches streamed events
- **Location**: `services/orchestrator/cmd/orchestrator/main.go`
- **Verification**: Run a chat turn; inspect Mongo
- **Priority**: High (dependency order: execute after AU-4/AU-5)

**AU-9: Consume new stream schema** (Status: Pending)
- **Area**: Frontend
- **Description**: Update UI to SSE stream event types (assistant_reply, tool_start, tool_update, tool_end, planner_thought)
- **Location**: Front service/UI
- **Verification**: Manual run
- **Priority**: Medium (dependency order: execute last)

**AU-10: Architecture + migration notes** (Status: Pending)
- **Area**: Docs
- **Description**: Ensure ARCHITECTURE.md and migrate.md reflect inline ADA, tool_specs usage, and removal of docs.json/toolsets
- **Location**: `docs/ARCHITECTURE.md`, `docs/migrate.md`
- **Verification**: N/A (docs)
- **Priority**: Low

---

## Action Items to Complete Migration

### High Priority Implementation Tasks

1. **Implement atlas.read executors (Chat, ADA)**
   - Suggested paths: `~/src/aura/executors/chat_agent/chat_atlas_read.go`, `~/src/aura/executors/atlas_data_agent/atlas_read.go`
   - Map tool args → AD method payloads; set session using `ToolCallMeta.SessionID`
   - Map AD method results → tool result data only
   - Cover at minimum: explain_control_logic, get_alarms, get_control_context, get_current_time, get_device_snapshot, get_equipment_status, get_time_series, get_topology, resolve_sources, list_active_alarms, list_created_alarms_in_window, list_apps, list_building_shutdowns, list_device_setting_changes, list_app_setting_changes, list_devices, list_equipment_mode_changes, list_app_status_changes, get_user_details, get_temperature_summary
   - Tests: generated AD client mocks; table-driven field mapping checks

2. **Replace stub planners with real planners**
   - Chat planner: port to `planner.Planner` under `~/src/aura/services/chat-agent/planner` (PlanStart/PlanResume assemble messages, tool config from specs, streaming output)
   - ADA planner: port gating/selection loop to `~/src/aura/services/atlas-data-agent/planner` (produce ToolCalls only; no AD RPC inside planner)
   - Wire in orchestrator via generated registrations in `~/src/aura/services/orchestrator/cmd/orchestrator/main.go` and `agents_planners.go`

3. **Register remediation-planning agent**
   - Call `remediation_planning_agent.RegisterRemediationPlanningAgentAgent(...)` in orchestrator with a stub planner first; upgrade to real planner later

4. **Convert Knowledge Agent to a goa-ai agent**
   - Add Goa design: `Service("knowledge_agent")` with `Agent("knowledge_agent")` and `Toolset("knowledge.emit")` for emit tools (diagnostics summary, agent facts)
   - Generate code: `goa gen github.com/crossnokaye/aura/services/knowledge-agent/design` (then top-level `goa gen github.com/crossnokaye/aura/design`)
   - Register in orchestrator using generated `RegisterKnowledgeAgentAgent(...)` with a stub planner; wire real planner later
   - Update any prompt/emit paths to consume generated `tool_specs` (no docs.json)

5. **Wire memory/run stores and SSE bridge**
   - Configure goa-ai `features/memory/mongo` and `features/run/mongo` in orchestrator runtime options
   - Keep Pulse `RuntimeStreams`; add SSE bridge (or Pulse subscriber) for UI until `front` migrates

6. **Introduce policy/approvals**
   - Implement `policy.Engine` in `~/src/aura/services/orchestrator/policy`; port approval logic and caps from orchestrator approval code
   - Register engine in runtime options; emit labels/metadata; add unit tests

### Medium Priority Tasks

7. **Update front-end to new stream schema**
   - Consume `assistant_reply`, `tool_start`, `tool_update`, `tool_end`, `planner_thought` event types in `front`
   - Adjust progress UI using `ExpectedChildrenTotal` and parent/child IDs

8. **Remove legacy tool/plugin fallbacks**
   - Delete or fence `shared/tools/*`, `services/*/tools/defs/*`, and any `docs.json` consumers
   - Ensure prompts/builders consume only generated `gen/.../agents/.../specs`

9. **Build provider configs from specs**
   - Add `config_from_specs.go` per agent to derive model provider config from `tool_specs.Specs` (no manual JSON schemas)
   - Unit tests for config builders

10. **Align AD shapes and complete remaining executors**
    - Verify AD design types match shared tool types; `scripts/gen goa` and fix any mismatches
    - Finish mappings for time series, list apps, building shutdowns, temperature summary, EHS, equipment/app status changes, list devices

### Verification Tasks

11. **Streaming and persistence verification**
    - Run a chat turn end-to-end; verify Pulse event order and Mongo run/memory stores persistence
    - Confirm model-bound messages only include sanitized summaries + compact data

12. **Clean up and finalize**
    - Remove temporary build tags and stubs; update `docs/ARCHITECTURE.md` and this guide; ensure `scripts/gen` does not enable Goa docs plugin

---

## Current Status Summary

### Completed
- ✅ Agents DSL added to AURA (chat-agent, atlas-data-agent, remediation-planning-agent)
- ✅ Agents Toolsets replacing legacy Goa tools plugin
- ✅ Codegen aligned (deterministic and fail-fast)
- ✅ Orchestrator host wiring (goa-ai runtime initialized)
- ✅ Todos integration with Chat
- ✅ GA-2 through GA-8 (all Goa-AI tasks)
- ✅ GA-1 cancelled (use existing types for external MCP toolsets)
- ✅ AU-1 (Todos exposed to Chat)

### In Progress
- 🔄 Implement atlas.read executors + client and switch planner to call tools
- 🔄 Register additional agents (ADA, remediation-planning) with planners as they migrate
- 🔄 Add Mongo-backed memory/run stores and SSE bridge
- 🔄 Apply Extend pattern for server payloads
- 🔄 Strong contracts at run boundary

### Next Steps (from Progress section)
- ⏭️ Port chat/ADA planners to planner.Planner and wire policy/approvals
- ⏭️ Remove remaining legacy ToolSet plugin code paths

---

## Execution Order (from notes)

### Goa-AI items
All Goa-AI items completed. GA-1 cancelled (use existing types for external MCP toolsets).

### AURA items
AU-2 → AU-3 → AU-4/AU-5 → AU-7/AU-8 → AU-9
- **Pending**: AU-2 through AU-10 (all except AU-1)

---

## Critical Path Items

1. **AU-2** (AURA) - Remove legacy ToolSet/BRTool fallbacks (execute first)
2. **AU-3** (AURA) - Build provider config from specs (execute second)
3. **AU-4/AU-5** (AURA) - Align AD shapes and implement remaining executors (can execute in parallel)
4. **AU-7/AU-8** (AURA) - Policy and persistence (execute after AU-4/AU-5)
5. **AU-9** (AURA) - Frontend update (execute last)

