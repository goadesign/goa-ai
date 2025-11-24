# Chat Agent Consumer-Only Toolsets: Comply With Exporters

## Desired Outcome

- The `chat-agent` consumes ADA and Diagnostics toolsets strictly as a client, using the provider-exported tool IDs and schemas end-to-end.
- The `chat-agent` DSL no longer re-exports provider toolsets under `chat_agent.*`. It only includes toolsets that chat owns and exports itself (e.g., `todos` if chat is the exporter).
- The planner advertises provider tool IDs (e.g., `atlas_data_agent.ada.*`, `diagnostics_agent.diagnostics.*`) to the model.
- Strong contracts: agent-as-tool execution always schedules child runs on the provider agent’s worker queue (no inline fallbacks).
- No aliasing or mapping required between chat and provider tool names.
- Stream bridge: child tool events are surfaced on the parent run stream with `parent_tool_call_id` (handled by runtime).

## Plan (no assumed context)

1) Update the chat DSL to stop re-exporting provider toolsets
   - Remove `Toolset(ada.ADAToolset)` and `Toolset(diag.DiagnosticsExportsToolset)` from `services/chat-agent/design/agents.go`.
   - Keep only toolsets that chat actually exports (e.g., `todos` if chat owns it).

2) Regenerate code
   - Run the code generator to refresh aggregated tool specs and agent scaffolding.
   - Expected: `gen/chat_agent/agents/chat/specs/specs.go` no longer contains `chat_agent.ada.*` or `chat_agent.diagnostics.*`.

3) Verify planner tool advertisement
   - Ensure `services/chat-agent/planner/planner.go` advertises provider tool IDs to the model:
     - ADA specs from `gen/atlas_data_agent/.../specs/ada`
     - Diagnostics specs from `gen/diagnostics_agent/.../specs/diagnostics`
     - Chat-owned toolsets (e.g., `todos`) still under `chat_agent.*` if chat exports them.

4) Register provider toolsets at chat bootstrap
   - In `services/chat-agent/cmd/chat-agent/main.go`, register ADA and Diagnostics using their typed provider helpers:
     - `NewAtlasDataAgentToolsetRegistration(rt)` and `NewDiagnosticsAgentToolsetRegistration(rt)`
   - Set `JSONOnly = true` and install the aggregator for ADA using `runtime.ToolResultFinalizer` + `runtime.BuildAggregationSummary` so the final envelope is produced by an actual tool call.

5) Remove any lingering chat-namespaced ADA/Diagnostics usage
   - Ensure there are no references to `chat_agent.ada.*` or `chat_agent.diagnostics.*` outside of truly chat-owned exports.

6) Clean imports and build
   - Ensure imports reflect provider specs (no unused chat ADA/Diagnostics specs).

7) Lint and unit test
   - Run lint and tests; fix any issues found.

8) Validate runtime behavior
   - Chat advertises provider IDs.
   - Child runs are scheduled on provider workers (strong contract).
   - Stream bridge shows child tool events on the parent stream (via runtime).

9) Documentation polish
   - Capture the “consumer complies with exporter” principle in docs for future services.

10) Optional: convenience helpers
   - Consider adding a thin `RegisterUsedToolsets(rt)` helper that registers provider toolsets and chat-owned toolsets in one call.

## Progress Tracker

- [ ] 1. Remove ADA/Diagnostics re-export from chat DSL (consumer-only)
- [ ] 2. Regenerate code (goa/gen)
- [ ] 3. Verify planner advertises provider IDs (no chat re-export)
- [ ] 4. Register provider toolsets in chat bootstrap (typed helpers)
- [ ] 5. Remove any lingering chat ADA/Diagnostics references
- [ ] 6. Clean imports and build
- [ ] 7. Lint and unit test (fix issues)
- [ ] 8. Validate runtime behavior (IDs, scheduling, stream)
- [ ] 9. Update docs to capture principle
- [ ] 10. Consider convenience helper for registration
