# MCP Apps host example

This example displays a generated Goa AI tool's HTML resource through the
official MCP Apps SDK. The Goa design owns tools, resources, validation and
private result metadata. The browser host owns its server connection, app tool
permissions and browser isolation. No second Goa AI browser protocol is added.

## Run acceptance

Use Go 1.27 and Node 26. From this directory:

```sh
npm ci
npx playwright install chromium --only-shell
npm run check
npm run prepare:fixture
npm test
```

Fixture preparation generates and builds the synthetic Goa server once. The
browser test reuses it. After changing only host code, rebuild the affected
browser bundle with esbuild instead of regenerating Go. Generated files,
browser bundles, cancellation evidence and test reports are ignored by Git.

To inspect the example manually, run `node server.mjs` after preparation and
open `http://127.0.0.1:43170`. Stop the process when finished. The four loopback
listeners belong to this example: host 43170, sandbox 43171, adversarial test
origin 43172, and generated MCP peer 43173.

## Contracts to retain in an application host

- Pin the official client to `2026-07-28`. Do not use automatic legacy
  negotiation or an SSE transport fallback. Apps `ui/initialize` remains the
  separate browser handshake owned by the Apps SDK.
- Construct `AppBridge` without an automatic client. Its automatic forwarding
  does not check tool visibility. Read the current catalog through the bound
  client before allowing an app call; reject tools without app visibility.
  The app cannot select another server or replace the host's credentials.
- Read the associated resource through that same connection. Check its exact
  URI and `text/html;profile=mcp-app` MIME type. The official schemas validate
  nested UI metadata; deprecated flat UI fields are not consumed.
- Render a separate-origin sandbox containing an opaque inner frame. The
  outer frame has the Apps-required script and same-origin permissions; the
  inner frame has only script permission. Check parent and child message
  sources and origins, and do not relay sandbox-control messages from views.
- Restrict the official browser transport to the configured sandbox origin.
  Window identity alone is insufficient after navigation. Private results
  must never be sent to a wildcard outer-frame destination.
- Enforce content and permissions policy in HTTP response headers. This host
  intentionally denies every external domain and browser permission, even if
  resource metadata requests them. It reports those approved restrictions in
  host context. Applications that grant access must compute and enforce only
  approved subsets of the resource's declared domains and permissions.
- Abort view-owned requests before teardown, close the bridge and remove the
  frame. Keep the shared MCP connection owned by the host. The byte proxy must
  carry browser cancellation through to the generated Goa endpoint.

The example grants only server tool calls. Resource forwarding, sampling,
external links and messages into model context are not advertised. An
application may implement those with its own permission decisions; app content
must not acquire authority to make those decisions itself. The synthetic peer
has no OAuth credential. Production hosts compose their authenticated client
and account-owned credential store separately; no credential enters the view.

## Verified paths

Chromium checks the full generated-server → official-client → sandbox →
official-App path: private result metadata, app-only tool calls, rejection of
model-only calls, blocked external connections and frames, host document
isolation, teardown and reopening. A waiting service method proves that
closing the view cancels the generated endpoint's HTTP context. A separate
adversarial page navigates the same frame between origins and checks incoming
request rejection, outgoing private-data protection and listener removal,
with a valid matching-origin counterexample.

See the [Apps specification](https://github.com/modelcontextprotocol/ext-apps/blob/82221c0c8ce7661efa6771c9d461511b1650495f/specification/2026-01-26/apps.mdx),
[official bridge](https://apps.extensions.modelcontextprotocol.io/api/classes/app-bridge.AppBridge.html)
and [official client protocol pinning](https://ts.sdk.modelcontextprotocol.io/v2/protocol-versions).
