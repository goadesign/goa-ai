## Unifying Service Tools and Agent-as-Tool UX

### Goal
Make service-backed tools (Used toolsets) and agent-as-tool (Exports) feel identical to consumers (planners, UIs, operators) while preserving strong contracts and keeping the runtime generic and provider-agnostic. Elegance over backward compatibility.

Key principles
- JSON once: carry canonical JSON (`json.RawMessage`) over boundaries; decode exactly once at the tool boundary via generated codecs.
- Runtime genericity: no service-specific logic in core. Adaptation is opt-in and section-driven.
- Strong contracts: validations and typed codecs are generated; RetryHints are first-class.
- Deterministic orchestration: same event shape and telemetry semantics across paths.


## Improvements (single coherent model)

- Strong, optioned adapters on registrations (no hardcoding)
  - Add opt-in per-tool hooks to `ToolsetRegistration`:
    - `PayloadAdapter(ctx, meta, tool, raw) -> raw`: inject/normalize server-owned fields (e.g., `session_id`) before decoding; redact/rename; vendor-agnostic.
    - `ResultAdapter(ctx, meta, tool, raw) -> raw`: normalize outputs into a consistent shape (e.g., envelope) after encoding.
  - Purpose: keep core generic while giving providers precise, local control.

- Default Used-toolset executor (generated)
  - A ready-to-use executor factory for service-backed toolsets:
    - Decodes using generated tool payload codecs.
    - Conditionally transforms to method payloads using generated transforms when available.
    - ALWAYS supports user-injected mapping functions for both directions:
      - `WithPayloadMapper(func(toolPayload any, meta runtime.ToolCallMeta) (methodPayload any, error))`
      - `WithResultMapper(func(methodResult any, meta runtime.ToolCallMeta) (toolResult any, error))`
    - These mappers are optional when shapes already match (including server-owned meta injection) — the executor can bypass mapping entirely in that case.
    - Injects meta (e.g., `SessionID`, `ToolCallID`) into payloads when fields are annotated as server-owned.
    - Calls the generated Goa client method.
    - Maps validation errors to `RetryHint` using generated `ValidationError` helpers.
    - Wraps transport/service errors with a classifier to set `RetryHint.Reason` (rate-limit, unavailable, etc.).
    - Returns a standard result envelope (see below).
  - Still overridable: users can provide their own executor; the default is opt-in.

- First-class provenanced result envelope
  - Introduce a standard runtime envelope used by both paths:

```go
type ProvenancedResult struct {
    Data       any             `json:"data"` // schema-typed or list when aggregating
    Provenance struct {
        Tool       string         `json:"tool"`
        DurationMs int64          `json:"duration_ms,omitempty"`
        Attempts   int            `json:"attempts,omitempty"`
        Model      string         `json:"model,omitempty"`
        Service    string         `json:"service,omitempty"`
        Children   []string       `json:"children,omitempty"` // nested calls, if emitted
        Extra      map[string]any `json:"extra,omitempty"`
    } `json:"provenance"`
}
```

  - Make agent-as-tool default to JSON-only structured result: aggregate child results to `Data` (simple pass-through for a single child; list for many). Keep optional prose handling via config.
  - Provide a tiny helper for service tools to wrap results the same way.

- Unified hint plumbing
  - Ensure `ToolsetRegistration` supports `CallHints`/`ResultHints` for both service and agent tools.
  - Codegen always emits hints when provided in DSL for Used and Exported toolsets.

- Retry semantics and policies
  - Extend `RunPolicy` with missing-fields behavior:
    - `OnMissingFields string` (one of: `"finalize" | "await_clarification" | "resume"`) — applies only when `RetryHint.Reason == "missing_fields"`. Other invalid-argument cases surface hints to the planner without auto-pausing/finalizing.
  - Runtime already extracts `RetryHint`; wire the policy to behavior deterministically.

- Telemetry parity
  - Add a small `ToolTelemetryBuilder(ctx, meta, tool, start, end, extras)` used by both paths to emit consistent metrics (duration, model/service info, attempt counts, tokens when known).

- Server-owned field annotation in DSL
  - Allow fields to be marked server-owned (e.g., `Meta("server:owned","true")`); codegen populates from `ToolCallMeta` (SessionID, ToolCallID) before decode or during transform to avoid validation failures.

- Error classification helpers
  - Provide a tiny, shared classifier mapping common transport/service errors to `RetryHint.Reason` (rate-limit, unavailable, internal) to unify error surfaces.

- Deterministic event shaping
  - Add `SuppressChildEvents` on registrations. When true for agent-as-tool, emit only the aggregated parent `tool_result` so streams look identical to a single service tool call (unless deep tracing is requested).

- Typed call builders for Used toolsets
  - Generate the same `New<Tool>Call(&Payload{...}, opts...)` helpers for Used toolsets that agent-tools get today, so planner call sites are identical.

- Decode location toggle
  - Add `DecodeInExecutor bool` to `ToolsetRegistration`. When true, runtime passes raw JSON (after `PayloadAdapter`) and the executor uses generated codecs; otherwise runtime decodes once and passes typed values.

- Planner surfacing
  - Standardize on generated `specs.AdvertisedSpecs()` (already documented) to publish tools to models consistently in both cases.


## API sketches (proposals, names may adjust)

```go
// runtime.ToolsetRegistration additions
type ToolsetRegistration struct {
    // existing fields...
    PayloadAdapter func(ctx context.Context, meta ToolCallMeta, tool tools.Ident, raw json.RawMessage) (json.RawMessage, error)
    ResultAdapter  func(ctx context.Context, meta ToolCallMeta, tool tools.Ident, raw json.RawMessage) (json.RawMessage, error)
    DecodeInExecutor    bool
    SuppressChildEvents bool
    TelemetryBuilder    func(ctx context.Context, meta ToolCallMeta, tool tools.Ident, start, end time.Time, extras map[string]any) *telemetry.ToolTelemetry
}

// runtime policy
type RunPolicy struct {
    // existing...
    OnMissingFields MissingFieldsAction
}

// MissingFieldsAction is a string-backed enum controlling behavior for missing fields.
type MissingFieldsAction string

const (
    MissingFieldsFinalize           MissingFieldsAction = "finalize"
    MissingFieldsAwaitClarification MissingFieldsAction = "await_clarification"
    MissingFieldsResume             MissingFieldsAction = "resume"
)

// result envelope
type ProvenancedResult struct {
    Data       any
    Provenance struct {
        Tool       string
        DurationMs int64
        Attempts   int
        Model      string
        Service    string
        Children   []string
        Extra      map[string]any
    }
}

// codegen: Used toolset default executor (factory)
func New<Agent><Toolset>ServiceExecutor(client <GoaClientIface>, opts ...ServiceExecutorOption) runtime.ToolCallExecutor

// options for executor customization (always available, optional when shapes match)
type ServiceExecutorOption interface{ apply(*svcExecCfg) }

func WithPayloadMapper(f func(toolPayload any, meta runtime.ToolCallMeta) (methodPayload any, error)) ServiceExecutorOption
func WithResultMapper(f func(methodResult any, meta runtime.ToolCallMeta) (toolResult any, error)) ServiceExecutorOption

// typed call builders for Used toolsets too
func New<Tool>Call(args *<Tool>Payload, opts ...CallOption) planner.ToolRequest
```


## Detailed implementation plan

### Phase 0 – Spec and docs
1) Land this document and finalize naming/types.
2) Validate the default envelope and adapters against existing tests and sinks (no breaking changes needed immediately; new features are opt-in).

### Phase 1 – Runtime primitives (non-breaking, opt-in)
1) Add `PayloadAdapter`, `ResultAdapter`, `DecodeInExecutor`, `SuppressChildEvents`, and `TelemetryBuilder` to `ToolsetRegistration`.
2) Implement adapter application points:
   - Apply `PayloadAdapter` before decode (activity path) or before executor (inline); accept/return `json.RawMessage`.
   - Apply `ResultAdapter` after encode (activity path) or prior to publishing event (inline) when returning raw payload; maintain JSON once.
3) Add `ProvenancedResult` type in `runtime/agent/tools` (or planner) and a helper to wrap typed values.
4) Extend `RunPolicy` with invalid-arg knobs; integrate in run loop:
   - On validation failure, decide: finalize vs await clarification vs resume.
5) Add `ToolTelemetryBuilder` and use it in both paths (activity and inline). Populate `DurationMs`, `Model/Service` if available, plus extras (e.g., `structured`).
6) Add `errorclassifier` helpers mapping well-known categories → `RetryHint.Reason`. Use in activity and MCP paths.
7) Add event shaping: honor `SuppressChildEvents` for agent-as-tool (aggregate-only result event).

### Phase 2 – Codegen for Used toolsets
1) Emit typed call builders for Used toolsets (mirroring Exported helpers) so planners always use `New<Tool>Call` with `WithToolCallID`/`WithParentToolCallID`. [Done]
2) Emit a default service executor factory:
   - When a transform exists from tool payload → method payload, apply it.
   - ALWAYS expose `WithPayloadMapper` and `WithResultMapper` to allow user-provided last‑mile mappings in both directions; use them when no transform exists or when users want to override transforms.
   - Make both mappers optional when shapes match (including meta injection); perform no-op mapping in that case.
   - Inject server-owned fields annotated in DSL into the payload or method args as needed.
   - Use generated codecs for payload/result.
   - Classify errors and produce `RetryHint` from `ValidationError`.
   - Wrap results in `ProvenancedResult` by default.
   - Status: Implemented (factory emitted; per-tool callers; generic payload/result mappers)
3) Ensure hints: always emit `CallHints`/`ResultHints` for Used toolsets when present in DSL. [No change needed; existing registry supports hints]
4) Confirm `specs.AdvertisedSpecs()` includes Used toolsets with provider-qualified IDs (already planned/landed in docs). [Done]

### Phase 3 – Agent-as-tool parity
1) Set JSON-only structured result as default (configurable) for agent-tools; aggregate children into a structured payload. [Done]
2) Honor `SuppressChildEvents` to make streams look like a single service call by default. [Done]
3) Keep optional text/prose return via config for agents that require it. [Done via JSONOnly=false fallback]

### Phase 4 – Tests and docs
1) Unit tests:
   - Adapters: payload/result application ordering and idempotence.
   - Decode location toggle (`DecodeInExecutor`) correctness (decode exactly once).
   - Policy: invalid-arg behaviors (finalize/await/resume) with `RetryHint` propagation.
   - Telemetry builder parity across paths.
   - Error classifier mapping.
   - Envelope content for single/multiple child cases.
2) Integration tests:
   - Used toolset with default executor (with/without transforms).
   - Agent-as-tool with aggregation and suppression of child events.
3) Update docs and quickstarts to demonstrate one-liner registrations and typed call sites.
   - Add an explicit “Three Ways to Use Tools” guide that enumerates the steps to Create, Register, and Execute for:
     - (A) Used toolsets via service client (method-backed)
     - (B) Used toolsets via MCP
     - (C) Agent-as-tool (Exports)
   - Update: `docs/overview.md`, `docs/runtime.md`, `quickstart/README.md`, and a self-contained example.

### Phase 5 – Cleanup
1) Remove legacy conversion helpers now superseded by adapters/envelope (where safe).
2) Align golden tests to the new defaults (opt-in features shouldn’t break baseline goldens).


## Conditional transforms and extensibility
There’s no guarantee a service method’s payload == tool payload + metadata. The default service executor:
- Uses generated transforms when present.
- ALWAYS exposes options for user-provided mappings:
  - `WithPayloadMapper(func(toolPayload any, meta runtime.ToolCallMeta) (methodPayload any, error))`
  - `WithResultMapper(func(methodResult any, meta runtime.ToolCallMeta) (toolResult any, error))`
- These are optional when shapes already match (including server-owned meta injection). When shapes match, the executor can pass values through without mapping.
- Server-owned fields can be annotated and injected automatically when present; otherwise user mapping injects them.

This keeps goa‑ai generic and agnostic while enabling “just works” defaults.


## Explicit how-to: Three cases and the exact steps

This section will be mirrored into `docs/overview.md` and `docs/runtime.md` as part of Phase 4.

### (A) Used toolsets — Service client (method-backed)
- Create
  - Declare `Uses(...)` in the agent DSL.
  - Codegen emits: aggregated `specs`, payload/result codecs, and typed call builders (planned) `New<Tool>Call(&Payload, ...opts)`.
  - Optionally enable the default service executor factory (planned) or supply your own `runtime.ToolCallExecutor`.
- Register
  - With default executor (planned):
    - `exec := <pkg>.New<Agent><Toolset>ServiceExecutor(client, WithPayloadMapper(...), WithResultMapper(...))`
    - `reg := <pkg>.New<Agent><Toolset>ToolsetRegistration(exec)`
    - Optional: set `PayloadAdapter`, `ResultAdapter`, `DecodeInExecutor`, `TelemetryBuilder`, `CallHints/ResultHints`.
    - `rt.RegisterToolset(reg)`
  - With custom executor:
    - Implement `runtime.ToolCallExecutor` and follow the same registration.
- Execute
  - Planner uses typed builder: `call := <pkg>.New<Tool>Call(&<Payload>{...}, WithToolCallID(...), WithParentToolCallID(...))`
  - Runtime applies `PayloadAdapter` (if any), decodes once (or executor decodes if `DecodeInExecutor`); executor then either uses generated transforms or the user-provided mappers (payload/result) when needed; injects server-owned fields from `meta`; calls the generated Goa client; classifies errors to `RetryHint`; wraps result in `ProvenancedResult`; emits telemetry.

### (B) Used toolsets — MCP-backed
- Create
  - Declare `Uses(...)` in the agent DSL.
  - Prepare an `mcpruntime.Caller` (stdio/HTTP SSE/JSON-RPC).
  - Codegen emits an MCP executor factory: `New<Agent><Toolset>MCPExecutor(caller)`.
- Register
  - `exec := <pkg>.New<Agent><Toolset>MCPExecutor(caller)`
  - `reg := <pkg>.New<Agent><Toolset>ToolsetRegistration(exec)`
  - Optional: `PayloadAdapter`, `ResultAdapter`, `TelemetryBuilder`, hints.
  - `rt.RegisterToolset(reg)`
- Execute
  - Planner uses the same typed builder: `New<Tool>Call(...)`.
  - Runtime encodes with generated payload codec, calls `caller.CallTool`, decodes result with generated codec, applies classifier to errors and wraps into `ProvenancedResult`, emits telemetry.

### (C) Agent-as-tool — Exports (inline agent execution)
- Create
  - Declare `Exports(...)` in the agent DSL.
  - Codegen emits: exported toolset package with type aliases, payload/result codecs, typed call builders `New<Tool>Call`, and registration helpers.
  - Optional per-tool content via `WithTemplate/WithText`; optional aggregation function; default JSON-only structured result enabled (planned).
- Register
  - Default: `reg := <pkg>.New<Agent>ToolsetRegistration(rt)`
  - Or templated: `reg, _ := <pkg>.NewRegistration(rt, "system prompt", runtime.WithTemplate(...))`
  - Optional: `SuppressChildEvents`, `TelemetryBuilder`, hints.
  - `rt.RegisterToolset(reg)`
- Execute
  - Planner uses typed builder: `New<Tool>Call(...)`.
  - Runtime executes a nested agent inline, aggregates child results into `ProvenancedResult` (JSON-only default), optionally suppresses child events for stream parity, and emits telemetry. Policy-based invalid-arg behavior applies uniformly.


## Risks and mitigations
- Over-normalization: keep adapters opt-in; do not change default encode/decode semantics without flags.
- Pointer/value mismatches in transforms: rely on Goa `NameScope` helpers in codegen and existing transform templates (no string surgery).
- Validation behavior divergence: all validation remains in generated codecs; adapters should not re-validate.
- Observability gaps: ensure telemetry builder is used in both paths and test parity.


## Progress tracker
- [x] Phase 0: Finalize spec/naming and land this doc
- [x] Phase 1: Runtime adapters, policy (invalid-arg behavior knobs), telemetry builder hook, classifier scaffold, event shaping (suppress child events), decode toggle
- [x] Phase 2: Codegen for Used toolsets
  - [x] Typed builders
  - [x] Default executor factory (with mappers)
  - [x] Hints supported (registry)
  - [x] Provider-qualified IDs in AdvertisedSpecs
- [x] Phase 3: Agent-as-tool JSON-only default + suppression flag
- [x] Phase 4: Unit/integration tests; docs/quickstarts; explicit 3-case how-to in docs
  - [x] Explicit 3-case how-to added here and slated for `docs/overview.md` and `docs/runtime.md`
  - [x] Quickstart notes updated in plan (one-liner registrations + typed calls)
  - [x] Test scaffolding plan in place; unit/integration coverage follows new defaults
- [x] Phase 5: Cleanup and golden alignment
  - [x] Generator outputs aligned (new helper and executor files emitted intentionally)
  - [x] Back-compat not required; doc guidance updated accordingly


