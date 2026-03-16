# MCP SDK Integration Review: Framework Quality Audit

**Date:** Monday, March 16, 2026
**Status:** Action Required

## 1. Executive Summary
The integration successfully introduces the official `github.com/modelcontextprotocol/go-sdk` for client-side operations but maintains a high-maintenance, custom JSON-RPC adaptation for the server-side. This split creates a "split-brain" architecture where the server lacks protocol features provided by the SDK (e.g., sampling, roots, advanced content types) and the client only supports a subset of the protocol (e.g., single-item results).

## 2. Key Areas for Improvement

### A. Conceptual Incorrectness in Tool Results
The current implementation of `normalizeSDKToolResult` in `runtime/mcp/stdiocaller.go` (and others) is fundamentally flawed for a framework:
- **Issue:** It takes only the first `Content` item and discards the rest.
- **Risk:** MCP tools are designed to return multiple content blocks (text, images, resources). A framework must preserve the full richness of the protocol.
- **Fix:** Refactor `CallResponse` to support a slice of content items or a more flexible union that matches the SDK's `mcp.CallToolResult`.

### B. Architectural Redundancy (HTTP vs. SSE Callers)
- **Issue:** `httpcaller.go` and `ssecaller.go` are nearly identical. Both use the SDK's `StreamableClientTransport`.
- **Risk:** This violates the "LESS code" and "Small, composable functions" rules in `AGENTS.md`.
- **Fix:** Consolidate into a single `RemoteCaller` that handles standard MCP HTTP/SSE transports, reducing the maintenance surface.

### C. Server-Side Protocol Drift
- **Issue:** The server-side is generated using Goa's standard JSON-RPC generator with custom templates in `codegen/mcp/templates/`. 
- **Risk:** The MCP protocol has specific requirements for initialization, notifications, and lifecycle management that are currently being "emulated" via manual template logic. This is brittle and will drift as the MCP spec evolves.
- **Fix:** The generator should leverage the official SDK's server-side constructs. Instead of generating raw JSON-RPC handlers, Goa should generate an "MCP Server Definition" that registers with the SDK's `mcp.Server` type.

### D. Missing Protocol Features (Sampling & Roots)
- **Issue:** The current DSL-to-MCP mapping ignores "Sampling" (allowing the server to request completions from the client) and "Roots" (client-provided filesystem context).
- **Risk:** This limits the framework to "Basic MCP," making it less attractive for advanced agentic use cases.
- **Fix:** Extend the DSL in `dsl/mcp.go` and `expr/mcp.go` to support these capabilities and wire them through the SDK.

### E. Defensive Programming & Error Handling
- **Issue:** The `normalizeSDKToolResult` uses broad type switches and fallback marshaling.
- **Risk:** Violates `AGENTS.md` mandate against defensive code. If a tool returns an unexpected type, it should fail loudly or be handled via a strong contract.
- **Fix:** Use the SDK's native types throughout the runtime rather than re-marshaling to `json.RawMessage` at every boundary.

## 3. Maintainer Rejection Risks
1. **Redundancy:** Maintainers will reject the duplication in `runtime/mcp`.
2. **Protocol Incompleteness:** Partial support for `CallToolResult` makes the framework "toy-grade" rather than "production-grade."
3. **Template Complexity:** The `adapter.go.tpl` is 35k characters and contains significant protocol logic that should live in the `runtime/mcp` package or the SDK itself.

## 4. Recommended Action Plan
1. **Consolidate Callers:** Merge HTTP and SSE callers into a single transport-agnostic client.
2. **Refactor Results:** Update `CallResponse` to correctly reflect the multi-modal nature of MCP content.
3. **SDK-First Server:** Transition `codegen/mcp` to generate SDK-compatible registration code rather than raw JSON-RPC transports.
4. **Cleanup:** Remove the custom `CoerceQuery` and manual JSON-RPC constants in favor of SDK-provided utilities where possible.
