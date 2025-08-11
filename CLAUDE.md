## CLAUDE Guide for MCP Plugin (Goa)

This file guides Claude when working on this repo. It captures intent, guardrails, and how to run generation. Read this first.

### High-level goals
- Treat MCP as a transport layered on top of Goa services.
- Generate an MCP-facing surface by composing on Goa codegen (don’t fork).
- Prefer small, deterministic transformations over big rewrites.
- Keep the output paths/imports aligned with Goa conventions.

### Core strategy (from prompts.txt)
- Assume services that use the plugin are pure MCP by default.
  - If any service method is not mapped to an MCP construct (tool, resource, prompt), fail generation with a clear error.
  - Filter away original transport files we no longer need (HTTP, gRPC, plain JSON-RPC) while keeping the essentials:
    - Service interface, endpoints, types
    - Encode/decode bits needed to unmarshal/marshal MCP payloads/results (reusing json.RawMessage tricks)
- Build an in-memory MCP service expression and let Goa generate service + JSON-RPC transport.
- Reuse Goa’s encoding/decoding to map MCP payloads to original payload types and vice versa for results.
- Apply minimal post-processing:
  - Move JSON-RPC files to `gen/jsonrpc/mcp/<service>/...`
  - Duplicate HTTP client paths to `gen/http/mcp/<service>/client/paths.go`
  - Update header imports/titles (modeled after Goa’s `updateHeader`).
- Ensure both server and client (and their CLIs) expose only MCP endpoints.

### File/dir conventions
- Service layer: as Goa generates it (kept intact).
- JSON-RPC transport for MCP: `gen/jsonrpc/mcp/<service>/...`
- HTTP path files for MCP clients: `gen/http/mcp/<service>/client/paths.go`
- Avoid contextual names like `mcp_direct.go`. Follow Goa naming patterns.

### Path handling
- Always use `codegen/pathutil.go` helpers (`normalize`, `replaceFirst`, `join`).
- No ad-hoc `ToSlash`/`FromSlash` in generation logic.

### Streaming
- Do not author custom MCP streaming templates.
- Rely on Goa JSON-RPC codegen for SSE/WebSocket when methods stream, then relocate/retitle.

### Header updates
- Use `updateHeader` (in `codegen/generate.go`) to change:
  - Title (HTTP → JSON-RPC) where relevant
  - Imports (gen/http → gen/jsonrpc)
  - Service import path (gen/<service> → gen/mcp/<service>) for the current alias
  - Add MCP HTTP client paths import to JSON-RPC `client/encode_decode.go`

### Generation entrypoints
- Provide exactly two public generation entrypoints:
  1) Generate for Goa “gen” (regular project code generation)
  2) Generate for Goa “example” (example scaffolding)
- Both should share the same core codepath and differ only in the context/wrapper used by Goa.
- Ensure both paths respect pure MCP rules and produce MCP-only CLIs.

### Coding standards
- Small, composable functions; avoid deep nesting.
- Descriptive names; no 1-2 letter identifiers.
- Prefer guard clauses and early returns.
- Keep templates minimal and transport-agnostic.
- Lint must pass (`golangci-lint`, v2 config) and `go build ./...` must be clean.

The Goa project favors short-ish files (ideally no more than 2,000 lines of code
per file).  Each file should encapsulate one main construct and potentially
smaller satellite constructs used by the main construct. A construct may consist
of a strut, an interface and related factory functions and methods.

Each file should be organized as follows:

1. Type declarations in a single type ( ) block, public type first then private
   types. The organization should list top level constructs first then
   dependencies.
2. Constant declarations, public then private
3. Variable declarations, public then private
4. Public functions
5. Public methods
6. Private functions
7. Private methods

Do not put markers between these sections, this is solely for organization.


### When in doubt
- Re-read `prompts.txt`, `DESIGN.md`, and Goa’s JSON-RPC CODEGEN docs.
- Favor composition over duplication.
- Keep the developer experience simple and predictable.


