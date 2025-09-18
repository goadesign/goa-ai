# Goa Assistant MCP — Design Specification

Purpose
- Provide a self‑contained, precise specification for an MCP server that helps users design, scaffold, and operate Goa systems following best practices (multi‑service layout, observability, diagrams, CI, and more).
- This document describes the MCP interface (tools) that the assistant exposes to LLM clients. It does not cover internal plugin mechanics.

Scope & Assumptions
- Default repository layout is a multi‑service workspace with a shared top‑level design that imports each service design.
- The assistant communicates over JSON‑RPC 2.0 via HTTP. Streaming uses Server‑Sent Events (SSE) negotiated by the `Accept: text/event-stream` header.
- Idempotency: Goa code generation is deterministic. `goa gen` overwrites generated files; `goa example` scaffolds and does not overwrite existing files.

Transport & Protocol
- Discovery: `tools/list` returns tool names and descriptions; arguments are discoverable via this spec and the tool’s advertised schema.
- Invocation: `tools/call` with a payload `{ "name": string, "arguments": object }`.
- Streaming tools: emit progress events as SSE notifications and a final response event.
- Standard progress event schema: `{ "phase": "planning|writing|generating|testing|done", "message": string, "percent"?: number }`.
- Cancellation: clients may cancel the HTTP request; server stops processing at next safe boundary and emits a final error event if possible.
- Error semantics:
  - JSON‑RPC: parse/invalid request/method not found/invalid params/internal errors mapped to standard codes (−32700, −32600, −32601, −32602, −32603).
  - Plain HTTP and gRPC mappings are applied to generated systems; see the systems handbook for details.

Naming Conventions
- Tool names are lowercase with dot separators (e.g., `system.new`, `scaffold.service`).
- Arguments use lowerCamelCase.

Defaults
- Layout: unified multi‑service (top‑level `design/` imports `services/*/design`).
- Observability: Clue + OpenTelemetry wiring when requested.
- Diagrams: Model (C4) optional but encouraged in platform creation.
- Diagram drift guard: the Goa Model plugin for drift checking is installed by default in new systems and wired into each service design.
- Mocking: All mocks are generated using Clue Mock Generator (cmg). Any tool that scaffolds mocks (e.g., clients.scaffold) invokes cmg; do not hand‑write mocks.

Compatibility
- Works with Go 1.22+, Goa v3.22.2+.

---

## Tool Catalog

The assistant exposes the following tools, grouped by category. Each entry lists:
- Name: the exact value passed in `tools/call.name` and shown to the LLM.
- Description: the human‑readable string advertised to the LLM.
- Arguments: names, types, whether required, and meaning.
- Streaming: whether progress is streamed; progress events carry `{ "message": string }`.

### A) System & Workspace

1) Name: `system.new`
- Description: Scaffold a complete multi‑service Goa system (unified layout with top‑level design importing per‑service designs). Generates repo tree, shared types, service stubs, scripts, CI, optional observability and diagrams.
- Arguments:
  - `module` (string, required): Go module path (e.g., `github.com/acme/myapi`).
  - `services` (array<string>, required): Names of services to create (e.g., `["users","products"]`).
  - `transports` (array<string>, optional): Default transports for services (e.g., `["http","grpc"]`).
  - `streaming` (bool, optional, default: false): Enable streaming scaffolds (SSE or gRPC streaming) in samples.
  - `observability` (object, optional): OTel/Clue setup; `{ otlpEndpoint?: string }`.
  - `ci` (bool, optional, default: true): Generate GitHub Actions with generation guard.
  - `diagrams` (bool, optional, default: true): Enable Model (C4) plugin and seed views.
  - `driftCheck` (bool, optional, default: true): Install and wire the diagram drift plugin in each service design.
  - `workspace` (string, optional, default: "unified"): `"unified"` or `"independent"` per‑service codegen.
 - `dryRun` (bool, optional, default: false): Plan changes without writing files.
- Streaming: yes (progress + final).
  Example: tools/call { name: "testing.configure", arguments: { path: "/work/myapi", services: ["users"] } }
  Example: tools/call { name: "clients.scaffold", arguments: { path: "/work/myapi", fromService: "orders", toService: "users", transport: "grpc" } }
  Example: tools/call { name: "scaffold.service", arguments: { module: "github.com/acme/myapi", service: "billing" } }
  Example: tools/call { name: "system.upgrade", arguments: { path: "/work/myapi", versions: { goa: "v3.22.2" } } }
  Example: tools/call { name: "system.new", arguments: { module: "github.com/acme/myapi", services: ["users","orders"] } }

2) Name: `platform.new` (alias of `system.new`)
- Description: Same behavior as `system.new`. Kept for backward compatibility.
- Arguments: same as `system.new`.
- Streaming: yes (progress + final).
  Example: tools/call { name: "testing.scenario.add", arguments: { path: "/work/myapi", service: "users", method: "list", transport: "http" } }
  Example: tools/call { name: "design.method.add", arguments: { path: "/work/myapi", service: "users", method: "list" } }
  Example: tools/call { name: "platform.new", arguments: { module: "github.com/acme/myapi", services: ["users"] } }

3) Name: `system.plan`
- Description: Dry‑run for `system.new` (and other scaffold operations); returns a precise change set of files/dirs and diffs.
- Arguments: same as `system.new`, but `dryRun` is implied `true`.
- Streaming: yes (progress + final).
  Example: tools/call { name: "design.conventions.apply", arguments: { path: "/work/myapi", conventions: { pagination: "cursor" } } }
  Example: tools/call { name: "system.plan", arguments: { module: "github.com/acme/myapi", services: ["users"] } }

4) Name: `system.apply`
- Description: Apply a previously produced plan; safe write with conflict checks.
- Arguments:
  - `path` (string, required): Root path where to apply.
  - `plan` (object, required): Plan emitted by `system.plan`.
  - `force` (bool, optional, default: false): Overwrite protected files (never overwrites `gen/`).
- Streaming: yes (progress + final).
  Example: tools/call { name: "workspace.module.add", arguments: { path: "/work/myapi", service: "payments" } }
  Example: tools/call { name: "system.apply", arguments: { path: "/work/myapi", plan: { /* from system.plan */ } } }

5) Name: `system.upgrade`
- Description: Upgrade Goa/Clue/plugin versions, run generation, tidy, and summarize diffs.
- Arguments:
  - `path` (string, required): Repo root.
  - `versions` (object, optional): `{ goa?: string, clue?: string }`.
  - `dryRun` (bool, optional, default: false).
- Streaming: yes (progress + final).
  Example: tools/call { name: "workspace.module.remove", arguments: { path: "/work/myapi", service: "payments" } }

6) Name: `system.validate`
- Description: Validate design/gen consistency, drift from diagrams, and forbidden edits under `gen/`.
- Arguments:
  - `path` (string, required): Repo root.
- Streaming: no.
  Example: tools/call { name: "plugin.explain", arguments: { detail: "summary" } }
  Example: tools/call { name: "docs.generate", arguments: { path: "/work/myapi", outDir: "docs/api" } }
  Example: tools/call { name: "diagrams.configure", arguments: { path: "/work/myapi", import: "github.com/acme/myapi/diagrams" } }
  
  Output contract:
  - `status`: "ok" | "error".
  - `checks`: array of { name: string, status: "pass"|"fail", details?: string } including:
    - `gen_drift` (design vs generated code out of date)
    - `diagram_drift` (Goa vs Model mismatch)
    - `gen_integrity` (edits under gen/ outside of codegen)
    - `imports` (missing/incorrect plugin imports for model/testing)
    - `middleware_order` (HTTP/gRPC ordering issues)
  - `actions`: recommended next steps (e.g., run `goa gen`, run `diagrams.generate`, fix imports).
  Example: tools/call { name: "system.validate", arguments: { path: "/work/myapi" } }

### B) Service & Design

7) Name: `scaffold.service`
- Description: Create a new Goa service repository or add a service folder within a unified repo using best practices.
- Arguments:
  - `module` (string, required): Go module path (if creating a new repo) or root module of unified workspace.
  - `service` (string, required): Service name (e.g., `payments`).
  - `transports` (array<string>, optional): Transports to enable (e.g., `["http","grpc","jsonrpc"]`).
  - `streaming` (bool, optional, default: false): Include streaming samples.
  - `ci` (bool, optional, default: true): Generate CI for a standalone repo.
  - `license` (string, optional): License identifier for new repos.
- Streaming: yes (progress + final).
  Example: tools/call { name: "plugin.new", arguments: { module: "github.com/acme/rate", name: "rate", features: ["dsl","codegen"] } }

8) Name: `design.method.add`
- Description: Add a method to an existing Goa service design, including payload, result, errors, transports, streaming.
- Arguments:
  - `path` (string, required): Filesystem path to repo root.
  - `service` (string, required): Target service name.
  - `method` (string, required): Method name to add.
  - `payload` (object, optional): JSON Schema‑like object describing payload attributes.
  - `result` (object, optional): JSON Schema‑like object describing result.
  - `errors` (array<object>, optional): List of domain errors (name, description, status/mapping hints).
  - `transports` (array<string>, optional): Transports to expose for this method.
  - `streaming` (string, optional, enum: `"none"|"http_sse"|"grpc_server"|"grpc_bidi"`, default: `"none"`).
- Streaming: yes (progress + final).

9) Name: `design.review`
- Description: Static review of the design with actionable suggestions (naming, error mapping, interceptors, streaming, security, versioning).
- Arguments:
  - `path` (string, required): Repo root.
- Streaming: no (returns a structured report).
  Example: tools/call { name: "design.review", arguments: { path: "/work/myapi" } }

### C) Code Generation

10) Name: `codegen.generate`
- Description: Run `goa gen` (and optional `goa example`), validate build/tests, and summarize changes.
- Arguments:
  - `path` (string, required): Repo root.
  - `example` (bool, optional, default: false): Run `goa example` after gen.
- Streaming: yes (progress + final).
  Example: tools/call { name: "codegen.generate", arguments: { path: "/work/myapi" } }

### D) Diagrams (Model / C4)

11) Name: `diagrams.configure`
- Description: Enable the Goa Model plugin; link services ↔ containers; seed C4 views.
- Arguments:
  - `path` (string, required): Repo root.
  - `import` (string, required): Model design import path.
  - `containerFormat` (string, optional): Naming format for containers (e.g., `"%s Service"`).
  - `excludedTags` (array<string>, optional): Tags to exclude from validation.
  - `complete` (bool, optional): Require all containers to have services.
- Streaming: no.
  Example: tools/call { name: "plugin.review", arguments: { path: "/work/rate-plugin" } }
  Example: tools/call { name: "http.cors.configure", arguments: { path: "/work/myapi", service: "users", origins: ["*"], methods: ["GET"] } }
  Example: tools/call { name: "diagrams.generate", arguments: { path: "/work/myapi", mode: "serve", generateDSL: true } }

12) Name: `diagrams.generate`
- Description: Generate (or refresh) the Model DSL from the Goa design, then start the `mdl` editor or export SVG / Structurizr workspace.
- Arguments:
  - `path` (string, required): Repo root.
  - `mode` (string, required, enum: `"serve"|"svg"|"structurizr"`).
  - `generateDSL` (bool, optional, default: true): If true, derive/refresh Model DSL from the Goa design before serving/exporting.
  - `outDir` (string, optional): Output directory for exported artifacts (SVG or workspace).
  - `stz` (object, optional): `{ id: string, key: string, secret: string }` for Structurizr uploads.
  - `import` (string, optional): If `diagrams.configure` wasn’t called, temporary Model design import path to write the DSL.
- Streaming: no.
  Example: tools/call { name: "observability.setup", arguments: { path: "/work/myapi", exporters: { otlp: "localhost:4317" } } }

### E) Observability (Clue + OpenTelemetry)

13) Name: `observability.setup`
- Description: Wire Clue logging, debug endpoints, health checks, and OTel exporters (HTTP & gRPC).
- Arguments:
  - `path` (string, required): Repo root.
  - `exporters` (object, optional): `{ otlp?: string, prometheus?: bool, jaeger?: string }`.
  - `debugPort` (int, optional): Port for debug endpoints (if separate).
  - `healthPort` (int, optional): Port for health checks (if separate).
  - `sampling` (object, optional): `{ rate?: number, adaptive?: { targetRPS: number, window: number } }`.
- Streaming: no.

### F) Quality & CI

14) Name: `quality.lint_test`
- Description: Run `golangci-lint`, `go vet`, and `go test ./...`; return a compact summary.
- Arguments:
  - `path` (string, required): Repo root.
  - `thresholds` (object, optional): `{ coverage?: number }`.
- Streaming: no.

15) Name: `ci.setup`
- Description: Create/update GitHub Actions workflow (tidy, gen guard, lint, test).
- Arguments:
  - `path` (string, required): Repo root.
  - `go` (array<string>, optional): Go versions matrix.
  - `cache` (bool, optional): Enable Go module/cache steps.
- Streaming: no.

### G) Security

16) Name: `security.configure`
- Description: Add JWT/OAuth2 patterns to the design and middleware/interceptors.
- Arguments:
  - `path` (string, required): Repo root.
  - `issuer` (string, optional), `audience` (string, optional), `header` (string, optional), `claims` (array<string>, optional).
- Streaming: no.

### H) Pulse (Event‑Driven Building Blocks)

17) Name: `pulse.explain`
- Description: Explain Pulse architecture (replicated maps, streaming, worker pool) and how to integrate it into Goa services.
- Arguments:
  - `detail` (string, optional, enum: `"summary"|"deep_dive"`).
  - `focus` (string, optional, enum: `"rmap"|"streaming"|"pool"`).
- Streaming: no.

18) Name: `pulse.rmap.new`
- Description: Scaffold a replicated map integration (dependency, wiring, examples, health checks).
- Arguments:
  - `path` (string, required): Repo root.
  - `service` (string, required): Service to integrate.
  - `mapName` (string, required): Replicated map name.
  - `keyType` (string, required), `valueType` (string, required): Types for entries.
- Streaming: yes (progress + final).

19) Name: `pulse.stream.new`
- Description: Scaffold a Pulse stream with topics; wire producers/consumers and observability.
- Arguments:
  - `path` (string, required): Repo root.
  - `service` (string, required): Service to integrate.
  - `stream` (string, required): Stream name.
  - `topics` (array<string>, required): Topics to create.
- Streaming: yes (progress + final).

20) Name: `pulse.worker_pool.configure`
- Description: Configure a worker pool with consistent hashing and health/metrics.
- Arguments:
  - `path` (string, required): Repo root.
  - `pool` (string, required): Pool identifier.
  - `workers` (int, required): Concurrency.
  - `keyField` (string, required): Field used for hashing.
  - `queue` (string, optional): Backing queue name.
- Streaming: yes (progress + final).

### I) Plugins (Extending Goa)

### J) Clients & Contracts

24) Name: `clients.scaffold`
- Description: Create a first‑class client for calling another Goa service within the same system, following best practices (narrow interface, domain types, transport‑independent factory, internal use of generated client, easy mocking).
- Arguments:
  - `path` (string, required): Repo root.
  - `fromService` (string, required): Calling (consumer) service name.
  - `toService` (string, required): Target (producer) service name to call.
  - `transport` (string, optional, enum: `"grpc"|"http"`, default inferred): Preferred transport for the client.
  - `package` (string, optional): Client package name; default derived from `toService`.
  - `methods` (array<string>, optional): Subset of target methods to expose; default: all.
  - `mocks` (object, optional): Mocks are always generated using Clue Mock Generator (cmg). Optional fields can tweak output location or package names.
- Streaming: yes (progress + final).

25) Name: `docs.generate`
- Description: Generate human‑readable service documentation (endpoints, payloads, error maps, examples) per service and as a unified index.
- Arguments:
  - `path` (string, required): Repo root.
  - `outDir` (string, optional): Output directory (e.g., `docs/api`).
- Streaming: no.

### K) HTTP/gRPC Configuration

26) Name: `http.cors.configure`
- Description: Configure CORS policies for HTTP services via the CORS plugin.
- Arguments: `path` (string, required); `service` (string, required); `origins` (array<string>); `methods` (array<string>); `headers` (array<string>); `credentials` (bool).
- Streaming: no.

27) Name: `http.middleware.add`
- Description: Add HTTP middleware (logging, auth, rate‑limit) in correct order.
- Arguments: `path` (string, required); `service` (string, required); `middlewares` (array<object> describing middleware).
- Streaming: no.
  
  Order‑of‑operations guidance:
  - Recommended: `otelhttp` → `debug.HTTP()` → `log.HTTP(ctx)` to ensure tracing context is established, debug toggles are applied, then request logging occurs with trace/span IDs.
  Example: tools/call { name: "http.middleware.add", arguments: { path: "/work/myapi", service: "users", middlewares: [{ name: "tracing" }, { name: "debug" }, { name: "log" }] } }

28) Name: `grpc.interceptors.add`
- Description: Add gRPC interceptors (auth/metadata/logging) for servers/clients.
- Arguments: `path` (string, required); `service` (string, required); `interceptors` (array<object>).
- Streaming: no.
  Example: tools/call { name: "grpc.interceptors.add", arguments: { path: "/work/myapi", service: "users", interceptors: [{ name: "log" }, { name: "otel" }] } }

29) Name: `diagrams.check`
- Description: Detect drift between Goa design and Model DSL; list discrepancies.
- Arguments: `path` (string, required); `failOnDrift` (bool, optional, default: true).
- Streaming: no.
  Example: tools/call { name: "diagrams.check", arguments: { path: "/work/myapi", failOnDrift: true } }

### L) Testing (Goa Testing Plugin)

33) Name: `testing.configure`
- Description: Enable the Goa testing plugin (goa.design/plugins/testing) and seed a basic test harness for services; wire integration test helpers.
- Arguments: `path` (string, required): repo root; `services` (array<string>, optional): limit configuration to specific services.
- Streaming: yes (progress + final).

Note: When client mocks are needed in tests, they are always generated via Clue Mock Generator (cmg) rather than hand‑written stubs.

34) Name: `testing.scenario.add`
- Description: Add a plugin‑based test scenario stub for a given service method.
- Arguments: `path` (string, required); `service` (string, required); `method` (string, required); `transport` (string, optional, enum: "http"|"grpc"|"jsonrpc").
- Streaming: yes (progress + final).

### M) Design Conventions & Workspace

35) Name: `design.conventions.apply`
- Description: Apply common design conventions (pagination, filtering, error types, ID types) across services.
- Arguments: `path` (string, required); `conventions` (object) with toggles and formats.
- Streaming: yes (progress + final).

36) Name: `workspace.module.add`
- Description: Add a service as a separate module and update `go.work`.
- Arguments: `path` (string, required); `service` (string, required).
- Streaming: yes (progress + final).

37) Name: `workspace.module.remove`
- Description: Remove a service module and update `go.work`.
- Arguments: `path` (string, required); `service` (string, required).
- Streaming: yes (progress + final).

21) Name: `plugin.explain`
- Description: Explain Goa plugin architecture (DSL, expressions, eval phases, codegen hooks) with examples.
- Arguments:
  - `detail` (string, optional, enum: `"summary"|"deep_dive"`).
- Streaming: no.

22) Name: `plugin.new`
- Description: Scaffold a new Goa plugin with `dsl/`, `expr/`, `codegen/`, `templates/`, and `plugin.go`, plus tests and an example.
- Arguments:
  - `module` (string, required): Module path.
  - `name` (string, required): Plugin short name.
  - `features` (array<string>, optional): e.g., `["dsl","codegen","example"]`.
- Streaming: yes (progress + final).

23) Name: `plugin.review`
- Description: Review a plugin repo and suggest improvements (eval phases, validation, file merge, templates, tests).
- Arguments:
  - `path` (string, required): Repo root.
- Streaming: no.

### N) Help & Discoverability

38) Name: `tool.help`
- Description: Return the argument schema and canonical examples for a given tool name to assist clients in constructing valid calls.
- Arguments:
  - `name` (string, required): Tool name (e.g., `system.new`).
- Streaming: no.

---

## Usage Examples

Initialize MCP session (one‑time per connection)
```json
{ "jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
  "protocolVersion": "2025-06-18",
  "clientInfo": { "name": "cli", "version": "0" }
}}
```

List tools
```json
{ "jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {} }
```

Call (streaming) — create a new system
```json
{ "jsonrpc": "2.0", "id": "sys-1", "method": "tools/call", "params": {
  "name": "system.new",
  "arguments": {
    "module": "github.com/acme/myapi",
    "services": ["users","products"],
    "transports": ["http","grpc"],
    "streaming": true,
    "observability": { "otlpEndpoint": "localhost:4317" },
    "ci": true,
    "diagrams": true,
    "workspace": "unified"
  }
}}
```

The server replies with SSE events: a series of `event: notification` lines with JSON data `{ "message": "..." }` followed by one `event: response` with a final `{ "message": "done" }`.

Enable diagrams and serve the editor (auto‑generates Model DSL first)
```json
{
  "jsonrpc": "2.0",
  "id": "dia-1",
  "method": "tools/call",
  "params": {
    "name": "diagrams.generate",
    "arguments": {
      "path": "/work/myapi",
      "mode": "serve",
      "generateDSL": true
    }
  }
}
```

Help for a specific tool
```json
{
  "jsonrpc": "2.0",
  "id": 7,
  "method": "tools/call",
  "params": {
    "name": "tool.help",
    "arguments": { "name": "system.new" }
  }
}
```

---

## Idempotency Guidance for LLM Clients
- Prefer `goa gen` to regenerate code whenever the design changes. It overwrites generated files deterministically.
- Use `goa example` to scaffold sample mains only when they don’t exist; it won’t overwrite user code.
- Never modify files under `gen/` manually.

---

## Appendix: Argument Schemas (JSON Schema)

The `tool.help` endpoint returns schemas similar to the examples below.

1) system.new arguments
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["module", "services"],
  "properties": {
    "module": { "type": "string" },
    "services": { "type": "array", "items": { "type": "string" } },
    "transports": { "type": "array", "items": { "type": "string", "enum": ["http","grpc","jsonrpc"] } },
    "streaming": { "type": "boolean" },
    "observability": {
      "type": "object",
      "properties": { "otlpEndpoint": { "type": "string" } },
      "additionalProperties": false
    },
    "ci": { "type": "boolean" },
    "diagrams": { "type": "boolean" },
    "driftCheck": { "type": "boolean" },
    "workspace": { "type": "string", "enum": ["unified","independent"] },
    "dryRun": { "type": "boolean" }
  },
  "additionalProperties": false
}
```

2) design.method.add arguments
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["path","service","method"],
  "properties": {
    "path": { "type": "string" },
    "service": { "type": "string" },
    "method": { "type": "string" },
    "payload": { "type": "object" },
    "result": { "type": "object" },
    "errors": { "type": "array", "items": { "type": "object" } },
    "transports": { "type": "array", "items": { "type": "string", "enum": ["http","grpc","jsonrpc"] } },
    "streaming": { "type": "string", "enum": ["none","http_sse","grpc_server","grpc_bidi"] }
  },
  "additionalProperties": false
}
```

3) clients.scaffold arguments
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["path","fromService","toService"],
  "properties": {
    "path": { "type": "string" },
    "fromService": { "type": "string" },
    "toService": { "type": "string" },
    "transport": { "type": "string", "enum": ["grpc","http"] },
    "package": { "type": "string" },
    "methods": { "type": "array", "items": { "type": "string" } },
    "mocks": { "type": "object" }
  },
  "additionalProperties": false
}
```

4) diagrams.generate arguments
```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["path","mode"],
  "properties": {
    "path": { "type": "string" },
    "mode": { "type": "string", "enum": ["serve","svg","structurizr"] },
    "generateDSL": { "type": "boolean" },
    "outDir": { "type": "string" },
    "import": { "type": "string" },
    "stz": {
      "type": "object",
      "properties": {
        "id": { "type": "string" },
        "key": { "type": "string" },
        "secret": { "type": "string" }
      },
      "additionalProperties": false
    }
  },
  "additionalProperties": false
}
```
