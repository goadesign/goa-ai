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
token handlers and assertions remain unchanged. Machine and enterprise setup
supplies the omitted registration issuer from fixture-owned configuration before
client startup. Enterprise setup also exports its trusted identity signing key and
seeded user, and adds `token_endpoint_auth_methods_supported: ["none"]` to IdP
metadata to match that handler's public-client authentication. Omission otherwise
means Basic under RFC 8414; production clients keep rejecting that inconsistency.

Browser authentication-method and issuer/resource validation scenarios receive
explicit synthetic registrations and their exact issuer. They test credential
placement and identity checks rather than dynamic registration, which the current
protocol removed. These are explicitly adapted scenarios, not stock referee
passes or a complete authorization-conformance claim.

From the repository root, create a local test certificate and build the driver:

```sh
umask 077
mkdir -p .cache/conformance-tls
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=localhost -addext 'subjectAltName=DNS:localhost,DNS:conformance-test.local,IP:127.0.0.1' -keyout .cache/conformance-tls/key.pem -out .cache/conformance-tls/cert.pem
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
callback validation, token exchange and authorized MCP dispatch. Repeat the command
with `--scenario auth/client-credentials-basic --force` and
`--scenario auth/client-credentials-jwt --force` to select the two machine
extensions explicitly. Without `--force`, this referee skips extension scenarios
and exits successfully without running their checks. The wire revision stays
`2026-07-28`. Signed registration uses the fixture's ES256 key and exact issuer
audience; its one-minute test assertion validity applies only to that assertion.

Select `--scenario auth/enterprise-managed-authorization --force` for identity
exchange and resource-grant redemption. The driver validates the host ID token's
signature, issuer, client audience, user, issuance and expiry before the production
enterprise transport receives it. The identity provider uses public authentication;
the independent resource issuer uses Basic. Only the resulting resource bearer
token is sent to MCP. The host key and issuer come from fixture configuration,
not token contents or MCP discovery.

The same command selects `auth/token-endpoint-auth-basic`,
`auth/token-endpoint-auth-post`, `auth/token-endpoint-auth-none`,
`auth/iss-supported`, `auth/iss-not-advertised`, `auth/iss-supported-missing`,
`auth/iss-wrong-issuer`, `auth/iss-unexpected`, `auth/iss-normalized`,
`auth/metadata-issuer-mismatch`, and `auth/resource-mismatch`. Explicit `--force`
retains every selected check at the current wire revision. Rejection scenarios
allow the client to exit with its actual error; their assertions require the
relevant metadata or callback to have been reached before declaring rejection.

The same command also selects `auth/scope-from-www-authenticate`,
`auth/scope-from-scopes-supported`, `auth/scope-omitted-when-undefined`,
`auth/scope-step-up`, and `auth/scope-retry-limit`. Their synthetic registrations
are supplied before client startup; original scope assertions and token handlers
remain unchanged. The production client obtains its initial challenge through
`server/discover`, then reuses established permissions until a resource rejection.

`auth/basic-cimd` supplies a real client document for the referee's fixed
`https://conformance-test.local/client-metadata.json` identifier. Host setup routes
only that named fixture destination to its configured local HTTPS server; URL,
Host and TLS identity stay unchanged. The local certificate must include the
extra DNS name shown above. `auth/offline-access-scope` publishes its document at
the actual HTTPS server URL, while `auth/offline-access-not-supported` uses an
explicit public preregistration. Both retain the original scope assertions. The
production client uses the existing metadata-registration constructor and decoder;
there is no runtime fixture exception or synthetic metadata response in the driver.

The pinned runner records checks before calling scenario cleanup. The supported
`offline_access` case fetches client grant metadata during cleanup, so that late
assertion is absent from its recorded result. Its informational grant-list check
is not independent verification. Local generated-client tests separately verify
registered refresh grants; no full conformance claim follows from this case.

The four `auth/metadata-*` cases receive their exact configured issuer from host
setup before startup. Their original discovery, resource-parameter and wire checks
run unchanged. Each still fails its required dynamic-registration assertion; these
are supported-path observations, not scenario passes. `auth/authorization-server-migration`
receives only its initial issuer registration. Its positive no-cross-issuer checks
run unchanged, while automatic dynamic re-registration remains an overall failure.
The transport rejects the issuer change instead of trusting it with old credentials.

The certificate's one-day validity is local test setup, not an OAuth token or
product retention rule. Keep the private key and raw reports out of commits:
reports contain synthetic secrets, authorization codes and tokens. After the
run, stop any fixture process and remove the local test key when no longer needed.

## Observed results

Verified on 2026-10-03 with the pinned referee:

| Role / scenario | Result | Limit of the evidence |
| --- | --- | --- |
| Client / `auth/pre-registration`, HTTPS-adapted, verified 2026-10-07 | 13 passed, no failures or warnings | Protected-resource and issuer discovery, S256 PKCE, preregistered Basic authentication, token exchange and authorized list/call through production OAuth. Other authorization profiles and negative cases remain open. |
| Client / `auth/client-credentials-basic` and `auth/client-credentials-jwt`, HTTPS-adapted, verified 2026-10-07 | 8 passed each, no failures or warnings | Explicitly selected machine extensions. Basic credentials and independently verified ES256 authentication, issuer discovery, token exchange and authorized MCP calls. Fixture setup supplies the exact registration issuer; no issuer is inferred from peer discovery. |
| Client / `auth/enterprise-managed-authorization`, HTTPS/metadata-adapted, verified 2026-10-07 | 9 passed, no failures or warnings | Signed host identity exchange, signed resource-bound grant redemption with Basic authentication, discovery and authorized MCP calls. IdP metadata is corrected to advertise its existing public-client handler. No SAML, identity-refresh or live-issuer claim. |
| Client / token-endpoint Basic, POST-secret and public authentication, HTTPS-adapted, verified 2026-10-07 | 18 passed each | Native production browser grant, exact credential placement, PKCE, resource parameter equality and authorized MCP calls. Host registration is explicit. |
| Client / valid advertised issuer and omitted issuer support, HTTPS-adapted, verified 2026-10-07 | 13 passed each | Browser authorization accepts matching issuer and the permitted absence of an unadvertised issuer parameter. |
| Client / missing advertised issuer, wrong issuer, unexpected mismatched issuer and normalized issuer variant, HTTPS-adapted, verified 2026-10-07 | 6 passed each | Metadata and callback reached, then no token exchange. Exact string comparison rejects a trailing-slash variant. |
| Client / issuer metadata mismatch and protected-resource mismatch, HTTPS-adapted, verified 2026-10-07 | 3 and 2 passed | Relevant metadata fetched, then authorization stops before using mismatched issuer endpoints or starting consent. |
| Client / initial challenge, metadata fallback and omitted-scope scenarios, HTTPS/preregistration-adapted, verified 2026-10-08 | 14 passed each; no warnings or failures | The production client selects the challenge before initial consent, uses metadata only as fallback and omits undefined scope. Runs took 0.93, 0.52 and 0.51 seconds. |
| Client / `auth/scope-step-up`, HTTPS/preregistration-adapted, verified 2026-10-08 | 25 passed; no warnings or failures | Initial challenge overrides metadata. A later resource rejection adds the challenged scope while retaining prior permissions. Corrected run took 0.94 seconds. |
| Client / `auth/scope-retry-limit`, HTTPS/preregistration-adapted, verified 2026-10-08 | 11 passed; no warnings or failures | Repeated insufficient-scope rejection stops after the permitted authorization recovery. Run took 0.51 seconds. |
| Client / `auth/basic-cimd`, HTTPS/document-host-adapted, verified 2026-10-08 | 14 passed; no warnings or failures | The production client fetches and validates the real document, uses its exact HTTPS identifier, and completes authorization and MCP dispatch. Run took 1.02 seconds. |
| Client / `auth/offline-access-scope`, HTTPS/document-host-adapted, verified 2026-10-08 | 13 passed; no warnings or failures | Omitting the optional scope is permitted. The referee's grant-list assertion remains informational because checks are recorded before its cleanup-time fetch. Run took 0.55 seconds. |
| Client / `auth/offline-access-not-supported`, HTTPS/preregistration-adapted, verified 2026-10-08 | 14 passed; no warnings or failures | The client does not request unsupported offline access. Run took 0.52 seconds. |
| Client / `auth/metadata-default`, `auth/metadata-var1`, `auth/metadata-var2`, `auth/metadata-var3`, HTTPS/preregistration-adapted, verified 2026-10-08 | 18, 18, 18 and 17 successful checks; one failure each; overall failures | All four complete authorization and MCP calls with no driver errors. Discovery covers OAuth metadata, both OpenID path arrangements and origin resource identity. Each original dynamic-registration assertion remains unmet. Runs took 0.98, 0.52, 0.52 and 0.52 seconds. |
| Client / `auth/authorization-server-migration`, HTTPS/preregistration-adapted, verified 2026-10-08 | 13 successful checks; one failure; overall failure | Both no-cross-issuer credential checks pass. The production client rejects changed issuer metadata before sending old credentials to it. Automatic dynamic re-registration remains unmet. Run took 0.52 seconds. |
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

The separate current-protocol SSE peer below verifies interrupted POST retries;
the official referee’s session-based `sse-retry` scenario is removed at
`2026-07-28` and cannot verify this contract. An explicit endpoint-trust
policy and a trusted read-only or idempotent declaration authorize bounded
retries of an interrupted HTTP 200 SSE response, with unchanged parameters and
a fresh request ID. Without that authorization, a lost tool response returns
`OutcomeUnknownError`. Completed errors and malformed complete frames are terminal.
The pinned SSE retry scenario excludes the new revision and tests the removed
session behavior, so it cannot settle that requirement. See the
[protocol conflict and required proof](../../docs/mcp_protocol_upgrade_plan.md#interrupted-http-responses-and-operation-ownership).

## Independent interrupted-POST peer

Build the existing Go client driver, then run:

```sh
go build -o .cache/mcp-conformance-client ./integration_tests/conformance/client
node integration_tests/conformance/sse_retry.mjs
```

This Node peer imports no Goa-AI implementation. It listens on localhost, starts
one production Go caller for each case, validates current body/header metadata,
and observes actual requests and synthetic effects over HTTP. It emits one
credential-free result per case and closes the child and sockets. A ten-second
deadline bounds one test case’s local resources; it is not a production timeout
or a limit on input rounds.

On 2026-10-07, all twelve cases pass in about 0.55 seconds in total: trusted
read-only and idempotent retries; untrusted and unsafe refusal; attempt exhaustion;
a later 401 retaining an unknown earlier outcome; host cancellation; completed
malformed, protocol-error and tool-error events; ordinary completion; and separate
allowances for two state-only input rounds. The idempotent operation has one
effect across three attempts, and the unsafe operation has one attempt and one
effect. Retries keep exact round parameters and use distinct JSON-RPC IDs. No GET,
protocol session or `Last-Event-ID` is used. Configured driver lint reports zero
issues; no root suite is repeated for these verification-only changes.

This is a locally authored independent peer, not a stock official-referee pass,
full transport tier or external deployment test. Its assertions implement the
[current POST transport](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http)
and the documented host-owned replay policy. Ordinary runtime boundary tests
retain broader event-framing and failure coverage.
