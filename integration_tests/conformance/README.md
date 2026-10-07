# Independent MCP verification

These drivers let the official MCP harness exercise goa-ai's production HTTP
caller and an actual generated server. They author synthetic domain calls; the
framework constructs the protocol messages. The server command lives in
`../fixtures/assistant/conformance/server` because it imports that fixture's generated
Goa contracts. Its explicit localhost Origin allowlist survives fixture regeneration.

The referee is [modelcontextprotocol/conformance](https://github.com/modelcontextprotocol/conformance)
at commit `c37eec888e1c6ff140af79987a40008548b7cc5f`, package
`0.2.0-alpha.12`. Every run selects wire revision `2026-07-28`. The dated official
schema and its independently checked hash are recorded in the
[upgrade plan](../../docs/mcp_protocol_upgrade_plan.md#reproducible-baseline).
Do not replace this pin with a moving package version or use the harness's default
revision. These are supported-path checks, not a full SDK tier or complete
released-requirement-set claim.

## Run

From the repository root, prepare the referee once:

```sh
mkdir -p .cache
git clone https://github.com/modelcontextprotocol/conformance.git .cache/mcp-conformance
git -C .cache/mcp-conformance switch --detach c37eec888e1c6ff140af79987a40008548b7cc5f
npm --prefix .cache/mcp-conformance ci --ignore-scripts
npm --prefix .cache/mcp-conformance run build
```

Regenerate and verify the fixture using the existing integration workflow, then
build the two drivers:

```sh
make itest
go build -o .cache/mcp-conformance-client ./integration_tests/conformance/client
go -C integration_tests/fixtures/assistant build -o ../../../.cache/mcp-conformance-server ./conformance/server
.cache/mcp-conformance-server --port 31172
```

Leave the server running while testing it in another terminal:

```sh
node .cache/mcp-conformance/dist/index.js server --url http://localhost:31172/rpc --scenario server-stateless --spec-version 2026-07-28 --output-dir .cache/conformance/server-stateless
node .cache/mcp-conformance/dist/index.js server --url http://localhost:31172/rpc --scenario dns-rebinding-protection --spec-version 2026-07-28 --output-dir .cache/conformance/server-origin
node .cache/mcp-conformance/dist/index.js server --url http://localhost:31172/rpc --scenario caching --spec-version 2026-07-28 --output-dir .cache/conformance/server-caching
```

For the client, the harness starts its own peer and supplies the URL:

```sh
node .cache/mcp-conformance/dist/index.js client --command .cache/mcp-conformance-client --scenario tools_call --spec-version 2026-07-28 --output-dir .cache/conformance/client-tools
```

Repeat the client command for `request-metadata`, `http-standard-headers`,
`http-custom-headers`, `http-invalid-tool-headers`, and `json-schema-ref-no-deref`.
An unsupported driver scenario is an error. No expected-failures file masks
mandatory failures. Stop the local server with an interrupt when done.

## HTTPS authorization fixture

The pinned referee binds its authorization fixtures to HTTP. The current MCP
contract requires HTTPS issuer and authorization endpoints; its loopback HTTP
exception applies only to browser redirects. `https_fixture.mjs` changes the
referee's listening socket and resulting base URL to HTTPS. The original command,
OAuth handlers, scenario and checks remain unchanged. This is an HTTPS-adapted
scenario, not a stock referee pass or a complete authorization-conformance claim.

From the repository root, create a local test certificate and build the driver:

```sh
umask 077
mkdir -p .cache/conformance-tls
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' -keyout .cache/conformance-tls/key.pem -out .cache/conformance-tls/cert.pem
go build -o .cache/mcp-conformance-client ./integration_tests/conformance/client
export MCP_CONFORMANCE_TLS_KEY="$PWD/.cache/conformance-tls/key.pem"
export MCP_CONFORMANCE_CA_FILE="$PWD/.cache/conformance-tls/cert.pem"
export NODE_EXTRA_CA_CERTS="$MCP_CONFORMANCE_CA_FILE"
export MCP_CONFORMANCE_DRIVER="$PWD/.cache/mcp-conformance-client"
export MCP_CONFORMANCE_SETUP="$PWD/integration_tests/conformance/https_fixture.mjs"
export MCP_CONFORMANCE_RESULTS="$PWD/.cache/conformance/authorization-https"
cd .cache/mcp-conformance
node --import tsx --import "$MCP_CONFORMANCE_SETUP" src/index.ts client --command "$MCP_CONFORMANCE_DRIVER" --scenario auth/pre-registration --spec-version 2026-07-28 --output-dir "$MCP_CONFORMANCE_RESULTS"
```

Both Node and the production Goa-AI transport trust that explicit test CA;
certificate verification stays enabled. The driver obtains its exact issuer and
registered credentials from the referee's typed scenario context. It supplies
host consent through the fixture's browser redirect and uses an explicit
process-lifetime credential store. Production OAuth code owns discovery, PKCE,
callback validation, token exchange and authorized MCP dispatch.

The certificate's one-day validity is local test setup, not an OAuth token or
product retention rule. Keep the private key and raw reports out of commits:
reports contain synthetic secrets, authorization codes and tokens. After the
run, stop any fixture process and remove the local test key when no longer needed.

## Observed results

Verified on 2026-10-03 with the pinned referee:

| Role / scenario | Result | Limit of the evidence |
| --- | --- | --- |
| Client / `auth/pre-registration`, HTTPS-adapted, verified 2026-10-07 | 13 passed, no failures or warnings | Protected-resource and issuer discovery, S256 PKCE, preregistered Basic authentication, token exchange and authorized list/call through production OAuth. Other authorization profiles and negative cases remain open. |
| Client / `tools_call` | 2 checks passed | Simple tool call and its wire schema |
| Client / `request-metadata` | 4 passed, 3 skipped, 1 warning; overall failure | Roots, sampling, and elicitation are unclaimed by this driver. The peer rejects `2026-07-28` while advertising that same revision as supported; it warns because the client stops instead of repeating the request. This is not an old-version fallback test. |
| Client / `http-standard-headers` | 3 passed, 8 skipped | Tool list/call method headers and tool name header. The driver does not exercise resource/prompt methods or removed initialization methods. |
| Client / `http-custom-headers` | 18 passed | Scalar encodings, Unicode, whitespace, control characters, header names, and omitted optional values |
| Client / `http-invalid-tool-headers` | 12 passed | Valid tool remains callable; invalid annotated tools are never invoked |
| Client / `json-schema-ref-no-deref` | 1 passed | Catalog compiled locally; network reference rejected without fetching the peer's canary |
| Server / `server-stateless` | 21 passed, 4 failed; overall failure | Four checks report `untestable`: fixture has no missing-capability, streaming-elicitation, or logging diagnostic tools. This is not a full scenario pass. |
| Server / `dns-rebinding-protection` | 2 passed | Configured localhost browser Origin accepted; attack Origin rejected |
| Server / `resources-read-binary` | 2 passed after binary-resource implementation | Real generated adapter returns a complete synthetic PNG; its wire schema passes. Empty binary content and aliases also have local generated-client/HTTP checks. |
| Server / `prompts-list`, `prompts-get-simple`, `prompts-get-with-args`, `prompts-get-embedded-resource`, `prompts-get-with-image` | 10 passed after typed-prompt implementation | Actual generated producers preserve argument text, the embedded URI, and ordered image/text messages. Each scenario passes its operation and wire-schema check. |
| Server / `completion-complete` | 2 passed after prompt-completion implementation | A typed service returns two suggestions and their total through the generated endpoint. Template-variable suggestions have local generated HTTP checks; this referee scenario selects a prompt. |
| Server / `caching` | 7 passed, 1 failed; overall failure | URI-template listing is not implemented or advertised. Fixed-resource and other tested cache fields pass. |
| Server / `resources-templates-read` | 2 passed after resource-template implementation | The typed service receives the exact expanded URI through the generated adapter. Lossy and overlapping declarations have local HTTP checks. |
| Server / `tools-call-with-progress` | 2 passed after progress implementation | Actual generated unary service emits 0/50/100 with the exact supplied token before its final result. Local race checks cover HTTP/stdio consumers, separate retry identities, callback failure and activity host visibility. |
| Server / `caching` after resource-template implementation | 8 passed | Template listing now returns the required cache fields through the generated endpoint. The earlier baseline failure remains in the full audit. |

The custom-header driver's second call omits the optional `verbose` property.
The harness's suggested JSON null conflicts with its own `type: boolean` schema;
this driver exercises the documented omission case with valid arguments. Local
transport tests independently verify null header omission. It does not claim a
successful invalid-argument call.

The broader released requirement set also covers additional content authoring, URI-template argument
suggestions, other authorization profiles and negative cases, server-produced multi-round input, and other
paths that this fixture does not implement. The complete released-set runs
exercised these scenarios and failed where the implementation or fixture is absent. See the [full audit](audit.md)
for scored and additional scenario counts. These drivers have not passed the full
suite. Local tests separately exercise recursive schemas, exact
header/body mismatch errors, explicit null structured output, all rich content
kinds, stdio cancellation, and durable form/URL/state-only input across successor
runs. The Temporal test runs production activities and real HTTP calls with a
new caller in each worker. It checks original arguments, exact host answers and
opaque state, distinct request IDs, and the final typed result using the SDK test
environment; it does not constitute a deployed-cluster test.

Interrupted SSE conformance remains a release gate. An explicit endpoint-trust
policy and a trusted read-only or idempotent declaration authorize bounded
retries of an interrupted HTTP 200 SSE response, with unchanged parameters and
a fresh request ID. Without that authorization, a lost tool response returns
`OutcomeUnknownError`. Completed errors and malformed complete frames are terminal.
The pinned SSE retry scenario excludes the new revision and tests the removed
session behavior, so it cannot settle that requirement. See the
[protocol conflict and required proof](../../docs/mcp_protocol_upgrade_plan.md#interrupted-http-responses-and-operation-ownership).
