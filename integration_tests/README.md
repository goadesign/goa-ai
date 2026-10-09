# MCP Integration Tests

These tests generate a small Goa application and exercise its generated Model Context Protocol (MCP) server through the public HTTP JSON-RPC endpoint.

## Covered contract

The assistant fixture declares the MCP surface currently supported by `goa-ai`:

- stateless `server/discover` and calls before discovery;
- per-request protocol/capability metadata and mirrored headers;
- unsupported-version and header-mismatch errors, with no version fallback;
- unary `tools/list` and `tools/call` operations;
- payload-free resources with fixed URIs through `resources/list` and `resources/read`;
- argument-free static prompts through `prompts/list` and `prompts/get`; and
- JSON-RPC error codes, including `-32602` for unknown tool names, resource URIs, and prompt names.

This YAML fixture does not cover the full generated MCP surface. Generated
contract tests under `codegen/mcp` verify input exchanges, native Tasks,
authenticated catalogs, URI readers, result views and subscription streams.
The [Apps host example](apps/README.md) verifies browser integration with the
official SDK and a generated server.

## Run the tests

From the repository root:

```bash
go test ./integration_tests/framework
go test ./integration_tests/tests -run 'TestMCP(Protocol|Tools|Resources|Prompts)$'
```

The integration runner regenerates the assistant fixture once per test process using the Goa version pinned in `fixtures/assistant/go.mod`, builds the generated example server, and starts an isolated server process for each scenario.

Set `TEST_SERVER_URL` to run scenarios against an already-running compatible server. Set `TEST_SKIP_GENERATION=true` only when the local fixture has already been regenerated and its generated example server is present.

## Layout

```text
integration_tests/
├── fixtures/assistant/       # Goa design, implementation stub, generated service, and example command
├── framework/                # HTTP JSON-RPC scenario runner
├── scenarios/
│   ├── protocol.yaml         # Stateless discovery, removed initialization, metadata and errors
│   ├── tools.yaml            # Unary tool discovery, calls, and argument validation
│   ├── resources.yaml        # Fixed resource discovery and reads
│   └── prompts.yaml          # Static prompt discovery and retrieval
└── tests/mcp_integration_test.go
```

Each YAML scenario contains optional default headers and ordered steps:

```yaml
scenarios:
  - name: resources_read_documents
    steps:
      - name: read
        op: ResourcesRead
        input: { uri: "doc://list" }
        expect:
          status: success
          result:
            contents:
              - { uri: "doc://list", mimeType: "application/json" }
```

Expected result objects are subset matches. This keeps scenarios focused on the protocol fields they intend to prove while allowing generated responses to include additional contract fields.

Independent official-harness drivers, their pinned revision, exact results, and
remaining conformance gaps are documented in
[Independent MCP verification](conformance/README.md).
