## Overview
This PR refactors the MCP SDK integration in the `goa-ai` framework to address severe multi-modal data loss and architectural redundancy identified during a code review. It replaces duplicate caller implementations with a unified, protocol-compliant `SessionCaller` heavily utilizing `github.com/modelcontextprotocol/go-sdk`.

## Key Changes
1. **Unified SessionCaller**: Replaced redundant and error-prone `StdioCaller`, `SSECaller`, and `HTTPCaller` structures with a single `SessionCaller` in `runtime/mcp/caller.go` that directly wraps `mcp.ClientSession`.
2. **Multi-Modal Content Fix**: Fixed a critical bug in `mcp_client_caller.go.tpl` and `caller.go` where `normalizeSDKToolResult` would silently discard all `CallTool` response content except the very first item. It now correctly iterates over the content array, concatenates strings into the primary `Result`, and safely serializes the raw structured output natively.
3. **No Defensive Programming Guideline Adherence**: Removed swallowed errors (e.g., `_ = json.Marshal()`) in caller responses and dropped extraneous nil checks on trace context constructs.
4. **Leveraging Protocol SDK**: Dropped duplicate `JSONRPCParseError` constants and the custom `mcpruntime.Error` type, routing straight to the official `jsonrpc` definitions to ensure proper cross-version protocol alignment.
5. **E2E Compatibility Test**: Added a strict unit test in `runtime/mcp/caller_test.go` that spins up an actual in-memory MCP SDK `ClientSession` connected to an `mcp.Server` to guarantee `CallTool` safely processes standard multi-block `TextContent` results.

## Why this is a net positive
Even without wholesale restructuring of the Goa server Codegen to entirely subsume JSON-RPC wrapping, this change safely removes duplicate types, significantly improves data handling correctness (multi-modal support for modern LLMs), and allows the client logic to undergo standard initialized connection handshakes (enabling version negotiation and capability exchange) courtesy of the true SDK session.
