// This setup selects verified HTTPS, supplies missing registration context,
// and makes enterprise identity metadata describe its existing public client.
// The original command and assertions still run; token handlers are unchanged.
import { createServer } from 'node:https';
import { readFileSync } from 'node:fs';
import { exportSPKI } from '../../.cache/mcp-conformance/node_modules/jose/dist/webapi/index.js';
import { EnterpriseManagedAuthorizationScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/enterprise-managed-authorization.ts';
import { ClientCredentialsBasicScenario, ClientCredentialsJwtScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/client-credentials.ts';
import { ClientSecretBasicAuthScenario, ClientSecretPostAuthScenario, PublicClientAuthScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/token-endpoint-auth.ts';
import { IssParameterSupportedScenario, IssParameterNotAdvertisedScenario, IssParameterSupportedMissingScenario, IssParameterWrongIssuerScenario, IssParameterUnexpectedScenario, IssParameterNormalizedVariantScenario, MetadataIssuerMismatchScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/issuer-parameter.ts';
import { ResourceMismatchScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/resource-mismatch.ts';
import { ServerLifecycle } from '../../.cache/mcp-conformance/src/scenarios/client/auth/helpers/serverLifecycle.ts';

const publicIdentityServers = new WeakSet();

const key = readFileSync(process.env.MCP_CONFORMANCE_TLS_KEY);
const cert = readFileSync(process.env.MCP_CONFORMANCE_CA_FILE);
ServerLifecycle.prototype.start = async function (app) {
  if (publicIdentityServers.has(this)) {
    // This IdP accepts the registered public client without a secret. Its omitted
    // list incorrectly selects RFC 8414's Basic default; advertise the real method.
    const json = app.response.json;
    app.response.json = function (body) {
      if (this.req.path === '/.well-known/openid-configuration') {
        body = { ...body, token_endpoint_auth_methods_supported: ['none'] };
      }
      return json.call(this, body);
    };
  }
  this.app = app;
  this.httpServer = createServer({ key, cert }, app);
  await new Promise((resolve, reject) => {
    this.httpServer.once('error', reject);
    this.httpServer.listen(0, resolve);
  });
  this.baseUrl = `https://localhost:${this.httpServer.address().port}`;
  return this.baseUrl;
};

// The original machine scenarios omit issuer from their host registration context.
// Supply it from their configured authorization server before spawning the client;
// discovery responses never establish trust for these registered credentials.
for (const scenario of [ClientCredentialsBasicScenario, ClientCredentialsJwtScenario]) {
  const start = scenario.prototype.start;
  scenario.prototype.start = async function (context) {
    const urls = await start.call(this, context);
    urls.context.issuer = this.authServer.getUrl();
    return urls;
  };
}

// The enterprise host receives its trust from fixture-owned configuration,
// including the signing key and seeded user. Neither MCP discovery nor a token
// being exchanged establishes this trust.
const enterpriseStart = EnterpriseManagedAuthorizationScenario.prototype.start;
EnterpriseManagedAuthorizationScenario.prototype.start = async function (context) {
  publicIdentityServers.add(this.idpServer);
  const urls = await enterpriseStart.call(this, context);
  urls.context.issuer = this.authServer.getUrl();
  urls.context.idp_public_key_pem = await exportSPKI(this.idpPublicKey);
  urls.context.idp_subject = 'demo-user@example.com';
  return urls;
};

// These scenarios test token placement and issuer/resource validation, not
// registration. Supply their synthetic host registration explicitly instead of
// invoking the removed dynamic-registration protocol. No peer metadata chooses
// authentication or establishes credential trust.
for (const [scenario, method] of [
  [ClientSecretBasicAuthScenario, 'client_secret_basic'],
  [ClientSecretPostAuthScenario, 'client_secret_post'],
  [PublicClientAuthScenario, 'none'],
  [IssParameterSupportedScenario, 'none'],
  [IssParameterNotAdvertisedScenario, 'none'],
  [IssParameterSupportedMissingScenario, 'none'],
  [IssParameterWrongIssuerScenario, 'none'],
  [IssParameterUnexpectedScenario, 'none'],
  [IssParameterNormalizedVariantScenario, 'none'],
  [MetadataIssuerMismatchScenario, 'none'],
  [ResourceMismatchScenario, 'none']
]) {
  const start = scenario.prototype.start;
  scenario.prototype.start = async function (context) {
    const urls = await start.call(this, context);
    urls.context = {
      issuer: this.authServer.getUrl(),
      client_id: 'test-client-id',
      client_secret: method === 'none' ? undefined : 'test-client-secret',
      token_endpoint_auth_method: method
    };
    return urls;
  };
}
