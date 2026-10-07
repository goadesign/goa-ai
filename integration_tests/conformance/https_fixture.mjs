// This local fixture setup changes only the referee's listening socket and
// resulting base URL and supplies missing machine-registration issuer context.
// Its original OAuth handlers and checks run
// through the original command; certificate verification remains enabled.
import { createServer } from 'node:https';
import { readFileSync } from 'node:fs';
import { ClientCredentialsBasicScenario, ClientCredentialsJwtScenario } from '../../.cache/mcp-conformance/src/scenarios/client/auth/client-credentials.ts';
import { ServerLifecycle } from '../../.cache/mcp-conformance/src/scenarios/client/auth/helpers/serverLifecycle.ts';

const key = readFileSync(process.env.MCP_CONFORMANCE_TLS_KEY);
const cert = readFileSync(process.env.MCP_CONFORMANCE_CA_FILE);
ServerLifecycle.prototype.start = async function (app) {
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
