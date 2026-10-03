# Complete released-set audit

This records the synthetic HTTP referee runs on 2026-10-03 at commit
`d2fa82fe19af5ea6d0ac7eba2908336ddce31f19`. It is a failing baseline, not a
conformance claim. The [driver instructions](README.md) name the pinned referee
and generated fixture. Each role completed and returned exit status 1.

Run the frozen requirement set, including its separately reported additional
scenarios, from the repository root with the generated server running:

```sh
node .cache/mcp-conformance/dist/index.js server --url http://localhost:31172/rpc --requirements 2026-07-28 --output-dir .cache/conformance-audit/server
node .cache/mcp-conformance/dist/index.js client --command .cache/mcp-conformance-client --requirements 2026-07-28 --output-dir .cache/conformance-audit/client
```

The server's 37 scored scenarios produced 74 successful checks, 30 failures,
3 warnings and 5 skips, plus one informational observation. Its 13 additional
scenarios produced 28 successes, 36 failures and one skip. The client's 32
scored scenarios produced 42 successes, 60 failures, one warning and 11 skips,
plus one informational observation. Its seven additional scenarios produced
15 failures and six skips. A scenario with no successful checks is not a pass.
Passing wire-schema checks alongside a failed operation do not verify that operation.

## What the failures establish

The client driver implements six scenarios. Unsupported scenarios terminate
explicitly, including authorization and request-state scenarios. Authorization
is also a framework gap: injecting an authenticated HTTP client does not
implement discovery, consent, grants or token management. Request-state echo
and durable agent continuation have local tests, but still require the referee
client scenario to establish independent coverage. Incidental positive checks
in the authorization-server-migration scenario do not prove authorization.

The server fixture exposes synthetic tools, resources and prompts under different
names from the referee's prescribed diagnostic endpoints. Unknown-tool or
unknown-resource failures therefore require fixture work as well as capability
work. Code tracing independently confirms absent binary resource authoring,
parameterized prompts, URI templates, argument suggestions, authored rich content,
progress production, server-produced additional input and Tasks. Add real
Goa-bound implementations rather than protocol-only replies in the fixture.

Four stateless diagnostics are untestable with the fixture. Some released tests
request deprecated sampling or roots methods; retain those failures explicitly
and verify the corresponding current elicitation contract independently. Do not
restore deprecated code just to satisfy a fixture. The metadata warning asks for
a repeat of the same revision after a contradictory version rejection; it does
not require supporting an older revision. The retry decision remains open.

All requested capabilities are release gates even when their tests are listed
as extensions. Apps, Skills and subscriptions need independent tests in addition
to this referee; their absence from its scenario list is not acceptance evidence.
The [upgrade plan](../../docs/mcp_protocol_upgrade_plan.md) owns the implementation
sequence and complete-path acceptance requirements.

## Subsequent binary-resource verification

After this baseline, binary resource authoring was implemented through the
existing Goa `Resource` DSL. The independent `resources-read-binary` scenario
passed 2/2 checks on 2026-10-03 at 18:07 UTC: the actual resource read and its
wire schema. This is a targeted improvement, not a rerun of the full suite.
The baseline table remains unchanged so it describes the recorded commit.
Local generated-client tests also verify empty blobs, byte aliases, malformed
base64 and both/neither representation fields. The real HTTP integration suite
covers the named image and empty binary resource alongside existing JSON reads.

## Scenario results

Success, failure, warning and skip columns count individual checks. Informational
observations are excluded from this table. “Additional” means the frozen referee
lists the scenario as an extension, pending or added after release.

### Server

| Scenario | Set | Success | Failure | Warning | Skip |
| --- | --- | ---: | ---: | ---: | ---: |
| `caching` | Scored | 7 | 1 | 0 | 0 |
| `completion-complete` | Scored | 1 | 1 | 0 | 0 |
| `dns-rebinding-protection` | Scored | 2 | 0 | 0 | 0 |
| `http-custom-header-server-validation` | Additional | 1 | 5 | 0 | 0 |
| `http-header-validation` | Additional | 14 | 0 | 0 | 0 |
| `input-required-result-basic-elicitation` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-basic-list-roots` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-basic-sampling` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-capability-check` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-ignore-extra-params` | Scored | 1 | 0 | 1 | 0 |
| `input-required-result-missing-input-response` | Scored | 1 | 0 | 1 | 0 |
| `input-required-result-multi-round` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-multiple-input-requests` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-non-tool-request` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-request-state` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-result-type` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-tampered-state` | Scored | 1 | 1 | 0 | 0 |
| `input-required-result-unsupported-methods` | Scored | 2 | 0 | 0 | 0 |
| `input-required-result-validate-input` | Scored | 3 | 0 | 0 | 0 |
| `json-schema-2020-12` | Additional | 1 | 1 | 0 | 0 |
| `prompts-get-embedded-resource` | Scored | 1 | 1 | 0 | 0 |
| `prompts-get-simple` | Scored | 1 | 1 | 0 | 0 |
| `prompts-get-with-args` | Scored | 1 | 1 | 0 | 0 |
| `prompts-get-with-image` | Scored | 1 | 1 | 0 | 0 |
| `prompts-list` | Scored | 2 | 0 | 0 | 0 |
| `resources-list` | Scored | 2 | 0 | 0 | 0 |
| `resources-read-binary` | Scored | 1 | 1 | 0 | 0 |
| `resources-read-text` | Scored | 1 | 1 | 0 | 0 |
| `resources-templates-read` | Scored | 1 | 1 | 0 | 0 |
| `sep-2164-resource-not-found` | Scored | 3 | 0 | 1 | 0 |
| `server-sse-multiple-streams` | Scored | 1 | 0 | 0 | 0 |
| `server-stateless` | Scored | 21 | 4 | 0 | 5 |
| `tasks-capability-negotiation` | Additional | 1 | 4 | 0 | 0 |
| `tasks-dispatch-and-envelope` | Additional | 3 | 6 | 0 | 0 |
| `tasks-lifecycle` | Additional | 1 | 8 | 0 | 0 |
| `tasks-mrtr-composition` | Additional | 1 | 1 | 0 | 0 |
| `tasks-mrtr-input` | Additional | 1 | 3 | 0 | 0 |
| `tasks-request-headers` | Additional | 2 | 3 | 0 | 0 |
| `tasks-request-state-removal` | Additional | 1 | 1 | 0 | 0 |
| `tasks-required-task-error` | Additional | 1 | 1 | 0 | 0 |
| `tasks-status-notifications` | Additional | 0 | 0 | 0 | 1 |
| `tasks-wire-fields` | Additional | 1 | 3 | 0 | 0 |
| `tools-call-audio` | Scored | 1 | 1 | 0 | 0 |
| `tools-call-embedded-resource` | Scored | 1 | 1 | 0 | 0 |
| `tools-call-error` | Scored | 1 | 1 | 0 | 0 |
| `tools-call-image` | Scored | 1 | 1 | 0 | 0 |
| `tools-call-mixed-content` | Scored | 1 | 1 | 0 | 0 |
| `tools-call-simple-text` | Scored | 1 | 1 | 0 | 0 |
| `tools-call-with-progress` | Scored | 1 | 1 | 0 | 0 |
| `tools-list` | Scored | 4 | 0 | 0 | 0 |

### Client

| Scenario | Set | Success | Failure | Warning | Skip |
| --- | --- | ---: | ---: | ---: | ---: |
| `auth/authorization-server-migration` | Scored | 2 | 1 | 0 | 0 |
| `auth/basic-cimd` | Scored | 0 | 1 | 0 | 0 |
| `auth/client-credentials-basic` | Additional | 0 | 1 | 0 | 0 |
| `auth/client-credentials-jwt` | Additional | 0 | 1 | 0 | 0 |
| `auth/dpop` | Additional | 0 | 3 | 0 | 0 |
| `auth/dpop-nonce` | Additional | 0 | 5 | 0 | 0 |
| `auth/enterprise-managed-authorization` | Additional | 0 | 2 | 0 | 0 |
| `auth/iss-normalized` | Scored | 0 | 1 | 0 | 0 |
| `auth/iss-not-advertised` | Scored | 0 | 1 | 0 | 0 |
| `auth/iss-supported` | Scored | 0 | 1 | 0 | 0 |
| `auth/iss-supported-missing` | Scored | 0 | 1 | 0 | 0 |
| `auth/iss-unexpected` | Scored | 0 | 1 | 0 | 0 |
| `auth/iss-wrong-issuer` | Scored | 0 | 1 | 0 | 0 |
| `auth/metadata-default` | Scored | 0 | 7 | 0 | 0 |
| `auth/metadata-issuer-mismatch` | Scored | 0 | 1 | 0 | 0 |
| `auth/metadata-var1` | Scored | 0 | 7 | 0 | 0 |
| `auth/metadata-var2` | Scored | 0 | 6 | 0 | 0 |
| `auth/metadata-var3` | Scored | 0 | 6 | 0 | 0 |
| `auth/offline-access-not-supported` | Scored | 0 | 1 | 0 | 0 |
| `auth/offline-access-scope` | Scored | 0 | 1 | 0 | 0 |
| `auth/pre-registration` | Scored | 0 | 1 | 0 | 0 |
| `auth/resource-mismatch` | Scored | 0 | 1 | 0 | 0 |
| `auth/scope-from-scopes-supported` | Scored | 0 | 1 | 0 | 0 |
| `auth/scope-from-www-authenticate` | Scored | 0 | 1 | 0 | 0 |
| `auth/scope-omitted-when-undefined` | Scored | 0 | 1 | 0 | 0 |
| `auth/scope-retry-limit` | Scored | 0 | 1 | 0 | 0 |
| `auth/scope-step-up` | Scored | 0 | 3 | 0 | 0 |
| `auth/token-endpoint-auth-basic` | Scored | 0 | 3 | 0 | 0 |
| `auth/token-endpoint-auth-none` | Scored | 0 | 3 | 0 | 0 |
| `auth/token-endpoint-auth-post` | Scored | 0 | 3 | 0 | 0 |
| `auth/wif-jwt-bearer` | Additional | 0 | 1 | 0 | 0 |
| `http-custom-headers` | Scored | 18 | 0 | 0 | 0 |
| `http-invalid-tool-headers` | Scored | 12 | 0 | 0 | 0 |
| `http-standard-headers` | Scored | 3 | 0 | 0 | 8 |
| `json-schema-2020-12-preservation` | Additional | 0 | 2 | 0 | 6 |
| `json-schema-ref-no-deref` | Scored | 1 | 0 | 0 | 0 |
| `request-metadata` | Scored | 4 | 0 | 1 | 3 |
| `sep-2322-client-request-state` | Scored | 0 | 5 | 0 | 0 |
| `tools_call` | Scored | 2 | 0 | 0 | 0 |
