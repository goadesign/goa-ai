## MCP Example TODOs

### Correctness & Semantics

- [x] Propagate original method parameters into resource URIs in the client
      adapter when mapping to `ResourcesRead` (e.g., forward `limit`, `flag`,
      `nums` for `GetConversationHistory`). Ensure encoding mirrors server-side
      query parsing so Goa validations still apply.
- [x] Define and enforce a clear initialization policy. Either require
      `initialize` before all methods (including `ping`) or explicitly allow
      `ping` pre-init; implement consistently.
- [ ] Decide and document semantics for `ToolsCallResult.IsError`. Either
      populate it for tool failures and handle on the client, or remove it to
      avoid ambiguity (prefer structured JSON-RPC errors).
- [x] Clarify `ContentItem` usage for structured JSON. For inline JSON, prefer
      `type: "text"` with `mimeType: "application/json"` (or a dedicated JSON
      type). If using `type: "resource"`, prefer `data` (base64) or `uri` over
      embedding JSON in `text`.

### Error Handling & Observability

- [x] Apply `mapError` consistently across adapter methods (tools, resources,
      prompts) and the stream bridge before returning errors, mapping to proper
      JSON-RPC codes. Preserve original error cause via `%w`.
- [x] Invoke `Logger` hooks on key events: request received, parameters decoded,
      service call start/end, stream send/sendAndClose, and error mapping.
- [x] Ensure JSON-RPC error code mapping is coherent across HTTP and SSE
      transports (`invalid_params`, `method_not_found`, internal errors).

### Streaming (SSE)

- [x] Pass the request context to the SSE encoder instead of
      `context.Background()` so deadlines/cancellation and logging context
      propagate.
- [x] Generalize Accept-based negotiation to support future streaming methods,
      not only `tools/call`.

### Client Adapter (wrapping generated Goa client)

- [x] Reduce encode/decode round-trips when building MCP tool arguments: use
      Goa encoders directly on the original payload to produce `params` bytes
      without constructing and decoding a `jsonrpc.Request` first.
- [x] Reuse a single original JSON-RPC client instance for fallback endpoints
      instead of constructing multiple instances.
- [ ] Extract a shared helper to "wrap Goa payload into MCP params" to remove
      duplication across each mapped tool.

### Server Adapter (MCP)

- [x] Remove or wire the unused `mux` field on `MCPAdapter`.
- [x] Document and plumb `ProtocolVersionOverride` and the
      `DefaultProtocolVersion`; ensure mismatches return a consistent error.
- [x] Improve `isLikelyJSON` detection or rely on explicit type/encoding rather
      than heuristics.

### Security

- [ ] Confirm `assertResourceURIAllowed` is applied consistently to all
      resource reads. Add tests for allowed/denied lists.
- [ ] Document default allow/deny behavior and guidance for secure configuration.

### Testing

- [ ] Add integration test verifying `GetConversationHistory` parameters are
      forwarded via MCP (URI query) and validated by Goa.
- [ ] Add SSE integration tests for `tools/call` covering notifications,
      final response, and error propagation.
- [ ] Add tests for batch handling: empty batch, single request, multiple
      requests; ensure array framing is correct.

### Performance

- [ ] Benchmark the `tools/call` path (client ↔ server) to quantify overhead
      from encode/decode and streaming. Use results to guide optimizations.

### Documentation

- [x] Document mapping semantics between original Goa endpoints and MCP tools
      and resources (how validations are preserved, how params are carried).
- [x] Document SSE JSON result carriage (text vs resource vs JSON typing) and
      client expectations.

### Maintainability & Naming

- [ ] Centralize helpers for MCP<->Goa payload/params conversion to reduce
      repetition.
- [ ] Review auto-generated helper names and reduce redundancy where feasible
      for readability without sacrificing generator clarity.


