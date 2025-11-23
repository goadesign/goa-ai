# Canonical JSON Tool Contracts for goa-ai

### Goal

Unify the tool payload/result story so that **between planners and the runtime everything is canonical JSON (`json.RawMessage`)**, and **all schema-aware decoding/encoding happens centrally via generated codecs**, simplifying hints, execution, and agent-as-tool composition.

### 1. Tighten core planner/runtime types in goa-ai

- **Change planner contracts to JSON (implemented):**
- `runtime/agent/planner/planner.go` now defines `ToolRequest.Payload` and `AwaitToolItem.Payload` as `json.RawMessage`. Any helper types that surface tool payloads are aligned to this JSON type.
- **Runtime usage sites (implemented):**
- `runtime/agent/runtime/workflow.go`, `runtime/agent/runtime/activities.go`, and `runtime/agent/runtime/agent_tools.go` all treat `ToolRequest.Payload` as canonical JSON. `ExecuteToolActivity` applies `PayloadAdapter` to `ToolInput.Payload` (a `json.RawMessage`), validates via `unmarshalToolValue` (payload codec), and still passes raw JSON into `ToolRequest.Payload`. `defaultAgentToolExecute` uses `marshalToolValue` to populate `RunContext.ToolArgs` as canonical JSON and uses `unmarshalToolValue`/`json.Unmarshal` only for prompt/template rendering.
- `marshalToolValue` and `unmarshalToolValue` are now strictly **JSON in → typed value out / typed value in → JSON out** helpers. Callers at the planner/runtime boundary always pass or receive `json.RawMessage`; any typed decoding happens in the runtime just before execution, hinting, or agent-as-tool finalization.

### 2. Standardize provider adapters to emit JSON-only tool payloads

- **Bedrock/OpenAI adapters (implemented in goa-ai):**
- `runtime/agent/model/model.go` now defines `ToolCall.Payload` as `json.RawMessage`.
- Bedrock adapters (`features/model/bedrock/client.go`, `features/model/bedrock/stream.go`) already emit `json.RawMessage` by decoding provider `toolUse.input` into raw JSON (`decodeDocument` / `decodeToolPayload`) and assigning that directly to `ToolCall.Payload`.
- The OpenAI adapter (`features/model/openai/client.go`) uses `parseToolArguments` to return `json.RawMessage` for `ToolCall.Payload`; tests (`features/model/openai/client_test.go`) now decode `resp.ToolCalls[0].Payload` as JSON rather than type-asserting to `map[string]any`.
- **MCP/gateway callers (implemented):**
- The gateway e2e test (`features/model/gateway/e2e_test.go`) and MCP assistant fixture (`integration_tests/fixtures/assistant/gen/mcp_assistant/register.go`) treat `ToolCall.Payload` / `ToolRequest.Payload` as canonical JSON bytes (copying `json.RawMessage` into `[]byte` for wire transport) rather than re-marshaling typed maps.
- **Document provider adapter contract (TODO – Step 6):**
- In `docs/runtime.md` (and/or a focused provider doc), add a short section stating: “Provider adapters MUST emit `json.RawMessage` for `ToolCall.Payload` matching the tool’s JSON args; planners and runtimes treat this as canonical JSON.”

### 3. Simplify all planners to pass through canonical JSON

- **goa-ai planners (implemented):**
- The generic stream bridge (`runtime/agent/planner/stream.go`) now passes `chunk.ToolCall.Payload` (a `json.RawMessage` from the provider adapters) directly into `ToolRequest.Payload` without any decoding or re-encoding.
- **Service-specific planners in AURA (implemented):**
- In each AURA planner’s streaming function (`services/atlas-data-agent/planner/planner.go`, `services/chat-agent/planner/planner.go`, `services/diagnostics-agent/planner/planner.go`, `services/remediation-planning-agent/planner/planner.go`, `services/knowledge-agent/planner/planner.go`), all codec-based decoding of `chunk.ToolCall.Payload` has been removed.
- Each planner now performs a single normalization step: when the provider payload is already `json.RawMessage`/`[]byte` it is copied into a fresh `json.RawMessage`; otherwise it is marshaled once and stored as `json.RawMessage`. `planner.ToolRequest{Payload: …}` is always built from that JSON, with no intermediate typed structs.
- `AwaitExternalTools` / `AwaitToolItem` construction in chat/other planners now uses `json.RawMessage` for `Payload` fields only; no typed payloads are threaded through `Await` structures.

### 4. Centralize all schema-aware decoding in the runtime (execution, hints, agent-as-tool)

- **Execution path (implemented):**
- `ExecuteToolActivity` now always treats `ToolInput.Payload` as canonical JSON (`json.RawMessage`). It applies `PayloadAdapter` to the raw bytes, optionally validates/decodes using `unmarshalToolValue(ctx, req.ToolName, raw, true)` (payload codec) to build structured `RetryHint`s, but still passes the raw JSON through to `ToolsetRegistration.Execute` via `planner.ToolRequest{Payload: raw}`.
- The only place payloads are decoded into typed structs before execution is inside the runtime, via `unmarshalToolValue`; call-site-specific decoding has been removed.
- Tool results from activities are always decoded with `unmarshalToolValue(ctx, toolName, out.Payload, false)` into typed result structs; if decoding fails, the raw JSON is preserved in `ToolResult.Result` for observability.
- **Hint path (implemented):**
- `runtime/agent/hooks/events.go` now defines `ToolCallScheduledEvent.Payload` as `json.RawMessage`, and runtime workflow code publishes scheduled events with `call.Payload` (canonical JSON).
- A new `hintingSink` in `runtime/agent/runtime/runtime_hints_sink.go` wraps the configured `stream.Sink`. It intercepts `ToolStart` events, decodes `ToolStartPayload.Payload` using the tool’s payload codec (falling back to generic `interface{}` JSON), and then calls `rthints.FormatCallHint` with the decoded **typed payload struct**. Other events are forwarded unchanged.
- `runtime/agent/runtime/hints/hints.go` remains a thin template registry; by feeding it typed payload/result structs from the runtime, all `CallHintTemplate`/`ResultHintTemplate` execute against Go field names (e.g., `.From`, `.DeviceAlias`, `.ScopeContext`) rather than raw JSON maps.
- `ToolResultReceivedEvent.Result` is populated with the typed result from `unmarshalToolValue`, and `stream.Subscriber` passes that structured value to `FormatResultHint`, so result hints also consistently see typed structs.
- **Agent-as-tool and finalizers (implemented):**
- Agent-as-tool executors (`defaultAgentToolExecute` in `agent_tools.go`) now:
  - Decode `call.Payload` for prompts/templates using `unmarshalToolValue` (when a tool spec exists) or `json.Unmarshal` into `map[string]any` as a fallback, while continuing to pass canonical JSON to nested agents via `nestedRunCtx.ToolArgs` (`marshalToolValue`).
  - Build `FinalizerInput.Children` from `RunOutput.ToolEvents` using the typed `ToolResult.Result` / `Error` values populated by the runtime.
- The finalizer tool invoker (`finalizer_invoker.go`) encodes its `payload any` argument once via `marshalToolValue(ctx, tool, payload, true)` and uses that JSON both for inline agent-tools (`ToolRequest.Payload`) and for the `ToolInput.Payload` passed to activities. It decodes activity results with `unmarshalToolValue` on the result codec before returning a structured `planner.ToolResult` to finalizers.

### 5. Clean up and align templates and toolsets with the new contracts

- **Service toolsets (atlas.read, chat AD subset, todos, diagnostics) – implemented in AURA:**
- `services/atlas-data/design/toolset.go` already authored `CallHintTemplate`/`ResultHintTemplate` against Go field names (e.g., `.ActivationIDs`, `.Sources`, `.RootDeviceAlias`, `.Depth`, `.From`, `.DeviceAlias`, `.StartingPoint`); no changes required.
- `services/chat-agent/design/ad_subset_toolset.go` and `services/todos/design/agents_toolsets.go` use Go field names (`.From`, `.To`, `.UserID`, `.Kind`, `.Items`, `.Merge`, etc.), and diagnostics exports/emit toolsets use `.AlarmID`, `.RunID`, `.Accepted`, etc. All of these now receive typed structs from codecs via the runtime hinting path.
- **ADA agent toolset hints – implemented in AURA:**
- `services/atlas-data-agent/design/toolset.go` has been updated so every `CallHintTemplate` uses the Go field names from the ADA tool payload types defined in `services/atlas-data-agent/design/requests.go`. Examples:
  - `{{.scope_context}}` / `{{.time_context}}` → `{{.ScopeContext}}` / `{{.TimeContext}}`
  - `{{len .sources}}` → `{{len .Sources}}`
  - `{{.starting_point}}`, `{{.direction}}`, `{{.max_depth}}` → `{{.StartingPoint}}`, `{{.Direction}}`, `{{.MaxDepth}}`
  - All other hints referencing `scope_context`, `time_context`, `focus`, etc. now line up with the corresponding Go fields.
- Generated helper templates under `gen/atlas_data_agent/.../agenttools/ada/helpers.go` will pick up these changes on the next Goa/agent codegen run and no longer reference snake_case JSON keys.

### 6. Update docs and examples to tell the simplified story

- **goa-ai docs:**
- In `docs/runtime.md` and/or a new “Tool Contracts” doc, describe the end-to-end story:
- Provider adapters emit `json.RawMessage` tool args.
- Planners always pass `json.RawMessage` in `ToolRequest.Payload`.
- The runtime is solely responsible for decoding/encoding via tool codecs.
- Hints and agent-as-tool flows always operate on typed payloads/results produced by codecs.
- **AURA docs:**
- Add a short section (e.g., in `docs/ARCHITECTURE.md`) summarizing how AURA services rely on these goa-ai contracts, so service authors know they never need to decode tool payloads in planners.

### 7. Regenerate and adjust tests

- **Regenerate goa-ai code where needed:**
- If any codegen templates in `codegen/agent` or `codegen/mcp` emit planner-related scaffolding that assumes `any` payloads, update templates to use `json.RawMessage` and rerun tests.
- **Fix and extend tests:**
- Update existing planner/runtime tests in goa-ai (e.g., `runtime/agent/runtime/runtime_tools_test.go`, `runtime/agent/stream/subscriber_test.go`) to assert that payloads are JSON, and that hints and executors see the expected typed values via codecs.
- In AURA, adjust planner tests (where present) to assert that they now pass `json.RawMessage` payloads through unchanged and no longer decode to typed payloads themselves.