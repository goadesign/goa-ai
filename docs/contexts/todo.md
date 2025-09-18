  MCP Plugin – Implementation Brief

  Repository Context

  - Plugin repo: mcp-plugin (you’re working here).
  - Goa repo: ~/src/goa (patched with JSON-RPC mixed ServeHTTP fix via FuncMap hasMixedTransports).
  - Example app: example/ within mcp-plugin (assistant service + MCP wrapper).

  Current State

  - MCP plugin provides:
      - DSL for MCP (Tools, Resources, StaticPrompt, DynamicPrompt, Notification, Subscription).
      - Codegen creates an MCP JSON-RPC service with appropriate endpoints and types.
      - Adapter that bridges MCP calls to the original Goa service methods.
      - Prompt provider interface and example implementation.
  - Templates hardened:
      - Unified payload decoding via goahttp.RequestDecoder for ToolsCall/ResourcesRead (both streaming/non-streaming).
      - PromptsGet implemented (static fallback + dynamic provider mapping).
      - Thread-safe initialization flag, quoted DSL strings, supportedProtocolVersion constant.
  - Dedupe of duplicate ServeHTTP in JSON-RPC mixed mode is fixed upstream via Goa patch (FuncMap hasMixedTransports + template guard). Plugin assumes Goa >= v3.22.2 (containing that patch).
  - Example builds cleanly with one ServeHTTP in mixed mode.

  Goals (Next Steps)

  1. Stabilize + Align

  - Set minimum Goa version to v3.22.2 in plugin README and optionally enforce a version gate at init time:
      - Soft gate: emit a clear runtime warning if detected generator/templates do not include hasMixedTransports in FuncMap.
      - Optional hard gate: add a build constraint or init() check to detect mixed duplication risk and abort with a helpful message.
  - Remove any leftover dedupe logic (already removed; confirm example.go no longer calls DedupeJSONRPCServers and plugin.go has no filepath/strings import used for dedupe).

  2. Codegen/DX

  - Improve toJSONSchema (codegen/mcp_schema.go):
      - Preserve validations: enum, pattern, min/max (number, length), minItems/maxItems for arrays where available on AttributeExpr.Validation.
      - For maps/arrays of user types, generate items/additionalProperties schema by recursing into the element attribute.
      - Keep compact and robust; avoid referencing non-exported Goa internals.
  - Tighten JSON-RPC path validation (codegen/mcp_expr_builder.go):
      - If original JSON-RPC service-level POST path is missing: record eval.Context error with a clear remediation (define JSONRPC(func(){ POST("/rpc") }) at service level).
      - Avoid panics; ensure fallback to /rpc remains only for documentation, not silently used.
  - Adapter encode/decode polish (codegen/templates/adapter.go.tpl):
      - Avoid unnecessary re-encoding: where result types match exactly, rely on goa encoder only once to produce JSON text.
      - Ensure error mapping consistently uses goa.PermanentError with proper JSON-RPC codes: invalid_params (-32602) for validation failures; internal (-32603) otherwise; method not found (-32601) for unknown
  tool/resource/prompt.

  3. Features

  - Prompts:
      - Expose argument schema for prompts/list: include arguments (array of PromptArgument) for static prompts if available; for dynamic prompts, include a generic shape or mark as unknown (or skip).
      - Permit multi-content items in prompts (MessageContent supports text/image/resource already in types; ensure adapter/provider paths can produce those).
  - Resources:
      - Add optional allow/deny URI list (configurable via adapter options) and per-resource validators before dispatching to original method.
  - Streaming:
      - In SSE handlers, consider including event IDs and basic heartbeat (document Last-Event-ID behavior; generator already supports SSE).
      - Ensure context cancellation via JSON-RPC cancellation maps to Go context cancel for streaming calls (document, ensure handlers respect ctx.Done()).

  4. Testing

  - Expand integration_tests:
      - tools.yaml: ToolsList/ToolsCall success, invalid params, unknown tool.
      - prompts.yaml: PromptsList/PromptsGet for static/dynamic; argument passing; error on missing name.
      - resources.yaml: ResourcesList/ResourcesRead; query param coercion (bool/int/float/arrays); invalid URI pattern.
      - streaming.yaml: SSE ToolsCall for a streaming method; interim “notification” events and final response; verify Accept negotiation behavior.
      - protocol.yaml: initialize handshake; unsupported protocol version; double initialize behavior.

  5. Docs + Examples

  - README (plugin root):
      - Requirements: Goa v3.22.2+ (JSON-RPC mixed handler fix).
      - Quickstart (DSL + JSON-RPC service-level POST).
      - Mapping table: MCP Tools/Resources/Prompts ↔ Goa Method / design constructs.
      - Error code mapping (JSON-RPC codes).
      - Streaming behavior and Accept header negotiation.
  - Example:
      - Prompt provider: include argument decoding for dynamic prompts.
      - CLI usage: initialize; tools/list; tools/call (HTTP+SSE); prompts/get; resources/read with URI params.

  6. Observability

  - Add optional options struct to NewMCPAdapter:
      - Logger hooks (request/response, redaction toggle).
      - ErrorMapper func(error) *jsonrpc.Error (advanced mapping).
      - Allow/Deny resource URIs.

  7. Release/CI

  - CI: run goa gen against this plugin with goa v3.22.2+; run integration_tests.
  - Tag plugin v0.1.0 with release notes, stating Goa minimum version.
  - Ensure README badges and notes up to date.

  Technical Notes

  - Goa dependency:
      - Assumed patch merged: server.go FuncMap contains hasMixedTransports; server_handler.go.tpl guards basic ServeHTTP accordingly.
      - If you need to detect absence of patch: scan generated server.go for duplicate ServeHTTP definitions and emit a warning at plugin run-time (optional).
  - File map (key components):
      - codegen/generate.go: orchestrates MCP service generation; ensures Endpoint/Client/JSON-RPC server files are added. Now avoid dedupe and rely on Goa.
      - codegen/mcp_expr_builder.go: builds temporary root, HTTP service for MCP JSON-RPC, prepares/validates/finalizes expressions; should hold the JSON-RPC path validation.
      - codegen/templates/adapter.go.tpl: core adapter logic for Tools/Resources/Prompts; keep encode/decode consistent and mapped.
      - codegen/templates/prompt_provider.go.tpl: interface for user prompt providers.
      - codegen/mcp_schema.go: toJSONSchema — enhance validations and coverage as noted.
  - Error mapping standard:
      - invalid_params (-32602): decode/validation issues, unknown prompt/resource/tool parameters.
      - method not found (-32601): unknown tool name/resource URI/prompt name.
      - internal (-32603): unhandled errors.
  - SSE events:
      - Goa JSON-RPC SSE emits:
          - event: notification (interim)
          - event: response (final)
          - event: error
      - Ensure adapter uses stream.Send / SendAndClose appropriately.
  - Param coercion function (adapter.go.tpl):
      - parseQueryParamsToJSON currently coerces to bool/int/float else string and aggregates repeats into arrays. Keep behavior and test it.
  - Versioning:
      - Maintain supportedProtocolVersion constant; consider allowing a list if needed.

  Concrete Tasks for Next Incarnation

  1. Remove any residual dedupe references:

  - Confirm codegen/example.go doesn’t call DedupeJSONRPCServers.
  - Confirm plugin.go imports don’t include filepath/strings unused by dedupe. If still present, remove.

  2. Add Goa version note and optional runtime warning:

  - README: require v3.22.2+.
  - Optional: in generator or example phase, detect absence of hasMixedTransports (by scanning server.go sections FuncMap? fragile) — probably skip runtime warning and rely on README.

  3. Enhance toJSONSchema:

  - Handle enum, pattern, min/max (numeric), minLength/maxLength (string), minItems/maxItems (array).
  - Recursively apply for arrays/maps of user types.

  4. Improve adapter error handling:

  - Standardize JSON-RPC error codes and messages; update templates.
  - Add optional adapter options struct with ErrorMapper and Logger hooks.

  5. Extend integration_tests scenarios as outlined.
  6. README and examples updates:

  - Add mapping table and improved Quickstart.

  7. Release:

  - After tests pass, tag v0.1.0 and note Goa minimum version.

  Assumptions

  - Goa v3.22.2 (with hasMixedTransports FuncMap + template guard) is merged (PR #3802).
  - The example builds after plugin update (verified).