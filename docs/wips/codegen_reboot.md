# WIP: Goa‑AI Codegen Reboot (First Principles)

This document proposes a clean-slate redesign of Goa‑AI code generation. It intentionally does **not** preserve backwards compatibility: we can break APIs, layouts, and wire formats to reach a conceptually correct end-state.

This document consolidates the prior tool codec/wire format rethink notes and the broader codegen reboot plan into a single WIP.

## Assumptions

This document assumes the Goa union redesign is already done:

- Goa OneOf unions are generated as JSON-native sum types with canonical `{ "type": "...", "value": <native JSON> }` encoding.
- OpenAPI emits `oneOf` + discriminator aligned with that shape.
- gRPC emits protobuf `oneof` + generated conversions between Go sum types and PB structs.
- Goa HTTP/JSON-RPC generators encode/decode unions directly (no `UnionToObject` wrapper plumbing).

## What Codegen Must Achieve (Requirements)

From the current goa.design docs (`content/en/docs/2-goa-ai/*`) and the runtime contracts, Goa‑AI codegen exists to produce:

- **Agent packages**: `gen/<service>/agents/<agent>/` with workflow definitions, plan/execute/resume activities, route metadata, and registration/client helpers.
- **Tool specs & schemas**: generated tool identifiers (`tools.Ident`), tool metadata, and JSON schemas used by planners, UIs, policy engines, and registries.
- **Tool codecs**: boundary decoding/encoding for tool payloads/results (and optional sidecars/artifacts), with deterministic failure modes and planner-facing `RetryHint` guidance.
- **Composition glue**: provider-side exports (agent-as-tool) and consumer-side wiring that yields a run tree with stable run links and predictable streaming topology.
- **MCP integration**:
  - server-side (Goa service → MCP suite exposed via JSON-RPC + SSE) and
  - client-side (agent consumes MCP tools via callers),
  with codegen-produced registration helpers and consistent error→`RetryHint` mapping.
- **Registry integration**: generated clients/adapters to discover and register toolsets, plus publication metadata for exported toolsets.
- **Docs scaffolding**: generated quickstart documentation that explains what was produced and how to wire it (optional).

### The most important boundary contract

Planners enqueue tool calls with **canonical JSON payload bytes**. Everything else (validation, decoding, executor dispatch, recording/storing) happens in the runtime.

Tool codecs are therefore *not* an internal convenience; they are part of the system’s durability story:

- payload/result bytes are persisted
- bytes are emitted on streams
- bytes are replayed during retries/resumes
- bytes must remain interpretable by UIs/tools/registries

## Goa Codegen Patterns We Must Follow

Goa already solved the “how to generate Go code sanely” problem. Goa‑AI must follow the same patterns and reuse the same data structures:

- **Generation model**: return `[]*goa.design/goa/v3/codegen.File`, each composed of named `SectionTemplate`s, rendered/merged by Goa’s generator pipeline.
- **Headers/imports/merging**: use `codegen.Header` as section 0 and let Goa merge imports/sections by file path.
- **Template organization**: embedded template FS + `template.TemplateReader`, no string-concatenation “builders”.
- **Type references**: always use `codegen.NameScope` helpers (`GoTypeRef`, `GoFullTypeRef`, `GoTypeName`, `GoTypeDef`), never `"*" + pkg + "." + Type`.
- **Transforms**: reuse Goa’s `codegen.GoTransform` and helper generation when compatible.
- **Transport data**: when binding tools to methods/transports, reuse Goa’s `codegen/service`, `http/codegen`, `grpc/codegen`, and `jsonrpc/codegen` data models rather than rebuilding parallel ones.

## First Principles: The New Mental Model

1. **Tools are contracts, not Go structs.**
   - The contract is the schema + tool metadata + a stable wire format.
2. **Tool wire bytes are the source of truth.**
   - Typed Go values are *derived views* used by executors, templates, and business logic.
3. **Codegen should be small and obvious.**
   - If the generator needs a clever algorithm, the design is wrong.
4. **No “patching generated code”.**
   - If we need different JSON-RPC/HTTP semantics, we generate goa‑ai‑owned files
     using Goa’s codegen data structures and our own templates. No post‑hoc edits.

## Proposed Architecture

### 1) A single intermediate representation (IR)

Introduce a goa-ai codegen IR package (conceptually: `codegen/ir`) that is the *only* input to templates.

The IR should be:

- stable and deterministic (sorted lists, explicit IDs)
- decoupled from template needs (no “just for rendering” fields)
- expressive enough to support:
  - native toolsets
  - method-backed toolsets (`BindTo`)
  - agent exports (agent-as-tool)
  - MCP toolsets
  - registry-backed toolsets

At minimum:

- `ServiceIR`: Goa service identity, path/pkg info (from `codegen/service.Data`)
- `AgentIR`: agent ID, run policy, workflow route, toolsets used/exported
- `ToolsetIR`: identity, provider kind, tools, registration metadata
- `ToolIR`: ID, description/tags, confirmation, bounded result, result reminder
- `ShapeIR`: payload/result/sidecar shapes with validations/examples
- `BindingIR`:
  - `MethodBinding` (service method reference + transport intent)
  - `AgentBinding` (provider agent route)
  - `MCPBinding` (suite/tool name + caller requirements)
  - `RegistryBinding` (registry name + namespace/version)

### 2) Generate tool specs once, at the owner

The current “specs emitted under the consumer agent” model forces deduplication hacks.

New rule:

- **Tool specs/codecs/schemas are generated exactly once per defining toolset** (service-owned toolsets, agent-exported toolsets, MCP suites, registry toolsets).
- Agents generate only:
  - **aggregators** (the set of specs they need), and
  - **registrations** (how to execute those tools in a given runtime).

This eliminates “clear SpecsDir to avoid duplicate emission” class bugs and makes ownership explicit.

### 3) Tool codecs & wire format (sum unions)

The tool codec story is currently the sharpest maintenance pain. The goal is to make the tool boundary boring: stable JSON, small codecs, deterministic unions, and failure modes that are easy for planners to recover from.

#### Problem statement

- **Exploding helper type graphs**: nested “JSON body” helper types are materialized across deep payload/result shapes.
- **Unbounded identifier growth**: helper type and helper function names encode full attribute paths (e.g. `...JSONAlarmsJSONItemJSONPointsAtActivation...`).
- **Union handling is brittle**: historically, Goa unions were represented as non-empty interfaces in generated Go types. `encoding/json` can marshal concrete values inside such interfaces, but it cannot unmarshal into them deterministically without additional type information, which cascades into ad-hoc wrapper/transforms and wire mismatches.

#### Principles

- **Single source of truth**: the Goa‑AI DSL defines the tool contract.
- **Stable contracts**: payload/result schemas and canonical bytes must remain durable across planners, UIs, workers, persistence, and replay.
- **Minimal codegen logic**: prefer Goa’s proven algorithms and types (especially JSON-native sum-type unions) over bespoke serialization logic.
- **Deterministic execution**: fail fast on contract violations; don’t guess via reflection or heuristic decoding.

#### Canonical union JSON shape

```json
{
  "type": "number_value",
  "value": 42.5
}
```

Where:

- `type` is a discriminator derived from the design (stable identifier for the union branch)
- `value` is the actual JSON value (not a JSON-encoded string)

Once Goa unions are sum types, the most robust default wire format is:

- Tool payload/result bytes are the canonical `encoding/json` encoding of the generated tool-facing Go types.
- Unions always use the canonical `{type,value}` shape above (Goa-owned).

A separate tool envelope (version fields, framing, provenance) is optional and should not exist solely to compensate for union decoding.

#### What codegen should generate

**Schemas**

- Ensure tool schemas describe the same JSON shape that `encoding/json` produces for the generated Go types.
- Union fields must be expressed as:
  - `type`: string enum of branch IDs
  - `value`: `oneOf` schema for branch payloads

**Codecs**

- Decode JSON into the generated tool-facing Go type via `encoding/json`.
- Call generated `Validate()` (including union `Validate()` enforcing exactly-one semantics).
- Convert validation issues into planner-facing `RetryHint` (missing fields, wrong shapes, allowed values, example input).
- Do not materialize deep helper type graphs solely for union decoding.

**Naming & ergonomics**

- Keep generated symbols stable and readable: no attribute-path concatenation in public identifiers.
- If helper types/functions are required (rare), use short base names + `NameScope.HashedUnique` for disambiguation.
- Prefer package-private helpers unless they represent a durable contract surface.

#### Cutover & cleanup (no backwards compatibility)

- Pick one union JSON shape (Goa sum-type unions) and make it the only supported encoding.
- Delete legacy code paths that exist solely to compensate for interface-based unions.
- Wipe/rotate stored bytes if needed rather than carrying dual-decode complexity forward.
- Treat schema/`Validate()` mismatches as bugs: fail fast rather than guessing.

#### Immediate implication for the current bug class

Errors of the form:

> cannot unmarshal number into Go struct field ... of type struct { Type *string; Value *string }

are symptoms of a union wire mismatch: some producers emit raw primitives while some decoders expect wrapper objects (or vice versa). Goa sum-type unions eliminate this by making the union representation explicit and JSON-native everywhere.

#### Next steps

1. Simplify goa‑ai tool codec generation to rely on `encoding/json` + generated `Validate()` for tool-facing types.
2. Delete union-specific helper graphs and any patching/adapters that exist only because unions were interfaces.
3. Add focused tests for tool payload union round-trips and `RetryHint` mapping.

### 4) Method-backed tools: reuse Goa transport models, not ad-hoc transforms

Method-backed execution is where today’s complexity explodes (especially around unions).

First-principles rule:

- Tool wire ≠ service Go types.
- Binding requires an explicit, generated adapter layer.

Implementation direction:

- Build adapters using Goa’s transport codegen data (`http/codegen`, `grpc/codegen`, `jsonrpc/codegen`) rather than reconstructing method shapes manually.
- Avoid generating deep “JSON body helper graphs” in goa‑ai. If we need body types/transforms, reuse Goa’s existing data/models and emit goa‑ai‑owned templates.

We can support `BindTo` in two tiers:

1. **Pure JSON-compatible payloads/results**: direct transform between tool payload type and method payload type (no unions). This can reuse `codegen.GoTransform`.
2. **Union-heavy shapes**: generate explicit adapter code that:
   - relies on Goa sum-type unions for deterministic decode/encode
   - constructs the appropriate method payload values
   - avoids deep helper graphs and attribute-path–scoped wrappers

The important constraint: this adapter logic must be generated in a small, testable way (per union type, not per attribute path).

### 5) MCP codegen: zero patching

Today’s MCP generator “patches” Goa-generated JSON-RPC output via string replacements. This must go.

New rule: **no string patching of generated code**.

We should:

- implement MCP JSON-RPC/SSE generation using Goa’s `jsonrpc/codegen` *data* but with goa‑ai‑owned templates.

### 6) Generated package layout (proposed)

Keep the high-level layout stable and unsurprising:

- `gen/<service>/agents/<agent>/` – workflows, registration, client
- `gen/<service>/toolsets/<toolset>/` – tool specs/codecs/schemas (service-owned)
- `gen/<service>/agents/<agent>/exports/<toolset>/` – tool specs + registration helpers (agent-owned exports)
- `gen/<service>/mcp/<suite>/` – MCP server/client wrappers + registration helpers
- `gen/<service>/registry/<name>/` – registry clients/adapters

Exact naming is negotiable; the invariant is: **specs live with the owner, agents only aggregate/use**.

## Plan (Rewrite Strategy)

### Phase 0: Decide the end-state contracts

- Adopt Goa sum-type unions as the baseline tool JSON contract (`{type,value}` for unions).
- Decide runtime boundary validation strategy (schema-first vs generated `Validate()`; prefer `Validate()` now that unions are JSON-native).
- Decide ownership layout (toolset-owned specs vs agent-owned specs).

### Phase 1: Introduce IR + rewire generators to consume it

- Implement IR builder from Goa roots:
  - reuse `codegen/service.ServicesData` for service identity/types
  - reuse transport `ServicesData` where bindings require it
- Add focused golden tests for IR determinism (stable ordering, stable IDs).

### Phase 2: Tool specs/codecs (standalone)

- Generate toolset-owned specs packages for:
  - standalone `Toolset(...)`
  - agent `Export(...)` toolsets
  - `MCPToolset(...)` suites (Goa-backed + inline)
  - registry-backed toolsets (schema only, codec is generic JSON)
- Update runtime/codecs to treat Goa union JSON as canonical; fail fast on schema/`Validate()` mismatches.

### Phase 3: Agent codegen consumes toolset specs

- Generate agent packages that only:
  - reference toolset specs (imports)
  - generate registrations and workflow route metadata
  - generate agent-as-tool executors that call provider agents (child runs)
- Delete consumer-side duplication of tool specs and all deduplication hacks.

### Phase 4: Method-backed adapters

- Implement the tiered adapter plan:
  - union-free: `codegen.GoTransform`
  - union-heavy: union-type–scoped adapter generation (no deep helper graphs)
- Add table-driven tests for the adapter generator using Goa test designs.

### Phase 5: MCP codegen rewrite

- Replace string patching with template-owned generation.
- Keep the generated API surface small:
  - `Register<...>Toolset(ctx, rt, caller)`
  - `NewCaller(client)` for Goa-backed clients
  - consistent error → `RetryHint` mapping

### Phase 6: Delete legacy codegen

- Remove:
  - deep JSON helper graph generator
  - string-patching utilities for generated files
  - duplicated “specs per agent consumer” emission paths
- Keep only:
  - IR builder
  - small, composable generators (toolsets, agents, MCP, registry)

---

## Progress Tracker

Last updated: 2025-12-16

### Summary

| Workstream | Status | Notes |
| --- | --- | --- |
| Tool codecs + unions | In progress | `encoding/json` codecs + generated sum-type unions landed in `codegen/agent/` |
| Codegen reboot | Not started | IR + owner-scoped toolset packages still pending |
| MCP codegen reboot | Not started | Replace string patching with template-owned generation |

### Phases

| Phase | Status | Notes |
| --- | --- | --- |
| Phase 0 — Decide end-state contracts | Done | Canonical tool JSON = `encoding/json` of tool-facing types; unions = `{type,value}` |
| Phase 1 — Introduce IR + rewire generators | Not started | |
| Phase 2 — Tool specs/codecs (owner‑scoped) | In progress | Tool codec + union simplification done; owner-scoped specs still pending |
| Phase 3 — Agents consume toolset specs | Not started | |
| Phase 4 — Method-backed adapters | Not started | |
| Phase 5 — MCP generation rewrite (no patching) | Not started | |
| Phase 6 — Delete legacy codegen | In progress | Union legacy removed; remaining legacy paths TBD |

### Implementation Log

- 2025-12-15: Generate sum-type union Go types for tool specs and simplify tool codecs around `encoding/json` (no legacy union wrapper plumbing), update goldens.
- 2025-12-16: Fix tool codec transforms for scalar payloads/results (always return `v` as a pointer), align union transform goldens, validate end-to-end via Aura `./scripts/gen goa` + `./scripts/lint`.
