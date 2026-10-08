// This setup selects verified HTTPS, supplies missing registration context,
// and makes enterprise identity metadata describe its existing public client.
// The original command and assertions still run; token handlers are unchanged.
import { createServer } from 'node:https';
import { AsyncLocalStorage } from 'node:async_hooks';
import { readFileSync } from 'node:fs';
import { exportSPKI } from '../../.cache/mcp-conformance/node_modules/jose/dist/webapi/index.js';
import { EnterpriseManagedAuthorizationScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/enterprise-managed-authorization.ts';
import { ClientCredentialsBasicScenario, ClientCredentialsJwtScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/client-credentials.ts';
import { ClientSecretBasicAuthScenario, ClientSecretPostAuthScenario, PublicClientAuthScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/token-endpoint-auth.ts';
import { IssParameterSupportedScenario, IssParameterNotAdvertisedScenario, IssParameterSupportedMissingScenario, IssParameterWrongIssuerScenario, IssParameterUnexpectedScenario, IssParameterNormalizedVariantScenario, MetadataIssuerMismatchScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/issuer-parameter.ts';
import { ScopeFromWwwAuthenticateScenario, ScopeFromScopesSupportedScenario, ScopeOmittedWhenUndefinedScenario, ScopeStepUpAuthScenario, ScopeRetryLimitScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/scope-handling.ts';
import { AuthBasicCIMDScenario, CIMD_CLIENT_METADATA_URL } from '../../.cache/mcp-conformance/src/scenarios/client/auth/basic-cimd.ts';
import { OfflineAccessScopeScenario, OfflineAccessNotSupportedScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/offline-access.ts';
import { metadataScenarios } from '../../.cache/mcp-conformance/src/scenarios/client/auth/discovery-metadata.ts';
import { AuthorizationServerMigrationScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/authorization-server-migration.ts';
import { ResourceMismatchScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/resource-mismatch.ts';
import { ServerLifecycle } from '../../.cache/mcp-conformance/src/scenarios/client/auth/helpers/serverLifecycle.ts';

const publicIdentityServers = new WeakSet();
const clientMetadataServers = new WeakMap();
const metadataHostAddresses = new AsyncLocalStorage();

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
  const metadata = clientMetadataServers.get(this);
  if (metadata) {
    // The host publishes the registered document. The production client and
    // offline-access referee fetch it over HTTPS before inspecting its grants.
    app.get('/client-metadata.json', (_request, response) => {
      response.type('application/client+json').json({
        client_id: metadata.identifier ?? `${this.getUrl()}/client-metadata.json`,
        client_name: 'Conformance host',
        redirect_uris: ['http://127.0.0.1:3000/callback'],
        token_endpoint_auth_method: 'none',
        grant_types: ['authorization_code', 'refresh_token'],
        response_types: ['code']
      });
    });
  }
  this.app = app;
  this.httpServer = createServer({ key, cert }, app);
  await new Promise((resolve, reject) => {
    this.httpServer.once('error', reject);
    this.httpServer.listen(0, resolve);
  });
  this.baseUrl = `https://localhost:${this.httpServer.address().port}`;
  metadataHostAddresses.getStore()?.push(this.baseUrl);
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
  [ResourceMismatchScenario, 'none'],
  [ScopeFromWwwAuthenticateScenario, 'none'],
  [ScopeFromScopesSupportedScenario, 'none'],
  [ScopeOmittedWhenUndefinedScenario, 'none'],
  [ScopeStepUpAuthScenario, 'none'],
  [ScopeRetryLimitScenario, 'none'],
  [OfflineAccessNotSupportedScenario, 'none']
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

// The CIMD referee names a non-resolving fixed host. Supply its exact local
// destination as host configuration; TLS still verifies that named identity.
// Offline access uses the actual HTTPS document URL so the referee can fetch it.
for (const [scenario, identifier] of [
  [AuthBasicCIMDScenario, CIMD_CLIENT_METADATA_URL],
  [OfflineAccessScopeScenario, undefined]
]) {
  const start = scenario.prototype.start;
  scenario.prototype.start = async function (context) {
    clientMetadataServers.set(this.authServer, { identifier });
    const urls = await start.call(this, context);
    urls.context = {
      issuer: this.authServer.getUrl(),
      client_id: identifier ?? `${this.authServer.getUrl()}/client-metadata.json`,
      metadata_address: identifier ? new URL(this.authServer.getUrl()).host : undefined,
      token_endpoint_auth_method: 'none'
    };
    return urls;
  };
}

// Metadata scenarios start their configured issuer first, then their resource.
// Retain that host configuration while starting each fixture; discovery responses
// cannot select a registered client's trusted issuer. Original checks stay intact,
// including their unmet dynamic-registration assertion.
for (const scenario of metadataScenarios) {
  const start = scenario.start;
  scenario.start = async function (context) {
    return metadataHostAddresses.run([], async () => {
      const urls = await start.call(this, context);
      const prefix = ['auth/metadata-var2', 'auth/metadata-var3'].includes(this.name)
        ? '/tenant1'
        : '';
      urls.context = {
        issuer: `${metadataHostAddresses.getStore()[0]}${prefix}`,
        client_id: 'test-client-id',
        token_endpoint_auth_method: 'none'
      };
      return urls;
    });
  };
}

// Migration supplies only the initial issuer's registration. A changed issuer
// cannot acquire those credentials; the original re-registration assertion stays
// unmet because this upgrade has no dynamic-registration compatibility path.
const migrationStart = AuthorizationServerMigrationScenario.prototype.start;
AuthorizationServerMigrationScenario.prototype.start = async function (context) {
  const urls = await migrationStart.call(this, context);
  urls.context = {
    issuer: this.as1.getUrl(),
    client_id: 'as1-client-id-LEAKED-IF-SEEN-AT-AS2',
    token_endpoint_auth_method: 'none'
  };
  return urls;
};
