MCP Plugin Release Notes

v1.0.0

First stable release of the Goa MCP plugin. Generate production‑ready MCP servers directly from your Goa design, with strong types, clear schemas, and first‑class streaming over JSON‑RPC.

Highlights
- Design‑first MCP: Tools, Resources, Prompts (static and dynamic), Notifications, and Subscriptions mapped from your Goa design.
- Streaming that “just works”: Mixed HTTP/SSE negotiated via `Accept`, backed by Goa v3.22.2 unified JSON‑RPC handler.
- Solid error semantics: Consistent JSON‑RPC codes across HTTP and SSE (`-32602` invalid params, `-32601` method not found, `-32603` internal).
- Schemas from types: Compact JSON Schema derived from Goa payloads for reliable tool inputs.
- Developer experience: Example server wiring, MCP client adapter, and comprehensive integration scenarios.

Compatibility
- Requires Goa v3.22.2 or newer (JSON‑RPC mixed transport and SSE fixes).
- Requires Go 1.24+.
- Module path is `goa.design/plugins/v3/mcp` (Semantic Import Versioning). Tags for Go modules must be `v3.x.y` even though this is the first stable plugin release.

What’s New Since Pre‑release
- Rely on upstream Goa for mixed JSON‑RPC ServeHTTP; no local dedupe/patching.
- SSE/HTTP error paths aligned and mapped to proper JSON‑RPC codes.
- Example streaming stubs simplified (progress + final) to match JSON‑RPC SSE framing.
- Improved JSON Schema generation (enums, patterns, min/max, collection sizes, recursive arrays/maps of user types).
- Adapter options for logging/error mapping and resource URI allow/deny lists.
- Integration tests expanded (protocol, tools, resources with coercions, prompts, notifications, SSE error events) and more robust runner (readiness probe, supervised child, bounded log tails).

Upgrade Notes
- Update your project to Goa v3.22.2+: `go get goa.design/goa/v3@v3.22.2`.
- Add the plugin: `go get goa.design/plugins/v3/mcp@v3.0.0` (first stable tag on the `v3` module path).
- Remove any local `replace` directives used during development.
- Re‑generate code: `goa gen <module>/design` (and `goa example` if you use the example server).
Transport
- Per the MCP specification, the generated server uses JSON‑RPC over HTTP and supports Server‑Sent Events (SSE) for streaming.

v0.1.0 (pre‑release)
- Require Goa v3.22.2+ (JSON‑RPC mixed transport fix; unified ServeHTTP).
- Remove JSON‑RPC dedupe/patch logic from generator; rely on Goa upstream.
- Improve JSON Schema generation (enum, pattern, min/max, minLength/maxLength, minItems/maxItems; recursion for arrays/maps of user types).
- Standardize adapter error mapping:
  - invalid_params (-32602) for decode/validation issues
  - method_not_found (-32601) for unknown tool/resource/prompt
  - internal (-32603) for unhandled errors (default handling)
- Add MCPAdapterOptions (Logger, ErrorMapper, allowed/denied resource URIs).
- Update README with Goa version requirement and mapping table.
- Align integration tests with error mapping; add resource param coercion scenario.
