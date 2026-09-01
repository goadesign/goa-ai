# MCP Integration Tests

These tests generate a small Goa application and exercise its generated Model Context Protocol (MCP) server through the public HTTP JSON-RPC endpoint.

## Covered contract

The assistant fixture declares the MCP surface currently supported by `goa-ai`:

- the `initialize` request followed by the `notifications/initialized` notification;
- the negotiated `MCP-Protocol-Version` header on that notification and later requests;
- unary `tools/list` and `tools/call` operations;
- payload-free resources with fixed URIs through `resources/list` and `resources/read`;
- argument-free static prompts through `prompts/list` and `prompts/get`; and
- JSON-RPC error codes, including `-32602` for unknown tool names, resource URIs, and prompt names.

The fixture does not cover dynamic prompts, arbitrary server notifications, resource subscriptions, query-bearing resource URIs, tool streaming, server-sent event responses, or a generated CLI. Those modes are not part of the supported generated MCP server contract.

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
│   ├── protocol.yaml         # Initialization, notification, version header, and JSON-RPC errors
│   ├── tools.yaml            # Unary tool discovery, calls, and argument validation
│   ├── resources.yaml        # Fixed resource discovery and reads
│   └── prompts.yaml          # Static prompt discovery and retrieval
└── tests/mcp_integration_test.go
```

Each YAML scenario contains optional default headers, an `auto_initialize` setting, and ordered steps:

```yaml
scenarios:
  - name: resources_read_documents
    pre:
      auto_initialize: true
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
