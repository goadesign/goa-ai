Title: Agent‑as‑Tool — Ideal Flow, Plan, and Progress

Overview

- Purpose: Define a clean, future‑proof contract for agent‑as‑tool (remote/external executor) versus local tool execution. Favor elegance and ownership: the executor decodes its own payloads; transports preserve bytes; planners route without binding to foreign types.
- Scope: DSL, expressions, and runtime behaviors in this repository. Codegen and app wiring follow naturally once these foundations exist.

Desired Outcome

- Single decode authority: only the executor of a tool decodes its payload/result using its own generated codecs/specs.
- Byte preservation: raw JSON payload/result is carried unchanged across boundaries. No implicit map[string]any conversions mid‑flight.
- Stable identity: tools are referenced by an unambiguous identifier. Routing is declarative; no name‑prefix surgery.
- Infer locality from the DSL: authors declare Toolset/Tool and which agent exports/uses them. The system infers whether execution is Local, RemoteAgent, or MCP without generic key/value metadata.
- Decode once, encode once: never pre‑decode and re‑encode across planner/transport/executor layers.
- Fail‑fast: precise errors when schema/codec decode fails at the executor. No silent fallbacks.

Conceptual Flow

- Model proposes a tool call: name + JSON args.
- Planner decides locality using declarative information from the design:
  - Local: bind to local types and execute in‑process or via a service client.
  - Remote (agent‑as‑tool): send identity + raw JSON; executor decodes and runs.
  - MCP: same as Remote; the MCP client owns decode/encode.
- Transport (Temporal/MCP/etc.) carries an envelope: identity + raw bytes (+ optional schema/version markers). It never deserializes inner payloads.
- Executor validates/decodes using generated codecs/specs, runs the implementation, encodes the result.

Plan (reader need not know prior context)

1) Expressions: capture toolset origin/provider
- Add ProviderKind enum: Local | RemoteAgent | MCP.
- Add ProviderInfo to ToolsetExpr: Kind, ServiceName, AgentName, ToolsetName. Add Origin pointer to reference the defining toolset when a reference is cloned.
- Infer ProviderInfo in validation: Local by default; MCP if declared via MCPToolset; RemoteAgent when a toolset reference originates from another service’s agent export.

2) DSL ergonomics: explicit cross‑service references
- Prefer Toolset(X) when you already have a design‑time expression for X; Goa‑AI infers a RemoteAgent provider when exactly one agent in another service exports a toolset with the same name.
- Add AgentToolset(service, agent, toolset) for explicitness or when inference is ambiguous (multiple exports share the same name), or when you don’t have an expression handle.

3) Runtime: byte‑preserving payload path
- When a ToolRequest.Payload is json.RawMessage or []byte, preserve it as‑is when scheduling activities. Do not re‑encode via codecs at this boundary. Decode at the executor (activity) using the generated codec.
- Keep existing codec path for typed local payloads (value → ToJSON).

4) Planner/runtime harmony (behavioral)
- ToolsetRegistration already supports DecodeInExecutor. The runtime passes raw JSON to executors when requested. Codegen can set this for RemoteAgent/MCP toolsets.

5) Tests & docs
- DSL tests: AgentToolset resolves the correct origin; ProviderKind inferred correctly.
- Runtime tests: preserve json.RawMessage payloads; existing codec tests continue to pass.

Progress Tracker

- [x] 1. Expressions: add ProviderKind, ProviderInfo, and Origin
- [x] 2. Expressions: infer ProviderInfo in validation
- [x] 3. DSL: add AgentToolset() with resolution and origin wiring
- [x] 4. DSL: preserve origin when cloning ToolsetExpr references
- [x] 5. Runtime: byte‑preserving marshalToolValue for json.RawMessage/[]byte
- [x] 6. Tests: add DSL tests for AgentToolset and provider inference
- [x] 7. Tests: ensure runtime codec tests remain green (no change needed)
- [x] 8. Docs: keep this document up to date
- [ ] 9. Lint + test: repository clean

Notes

- Elegance first: no free‑form Meta; use natural DSL surfaces and inference.
- Backcompat not required; we shape the DSL now to be world‑class.
- Codegen and routing tables can build on ProviderInfo later to set DecodeInExecutor and generate adapters; this plan focuses on the core foundations.
