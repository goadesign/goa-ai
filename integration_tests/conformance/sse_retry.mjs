// This peer checks current POST retries over real HTTP without importing Goa-AI
// transport code. Each case owns a localhost server and one Go caller process;
// wire assertions and side-effect counts decide whether the case passes.
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const driver = fileURLToPath(new URL('../../.cache/mcp-conformance-client', import.meta.url));
const revision = '2026-07-28';
const cases = [
  { name: 'trusted read-only', hints: { readOnlyHint: true }, attempts: 2, trust: true, losses: 1, calls: 2, outcome: 'complete' },
  { name: 'trusted idempotent', hints: { idempotentHint: true }, attempts: 3, trust: true, losses: 2, calls: 3, outcome: 'complete', effects: 1 },
  { name: 'untrusted read-only hint', hints: { readOnlyHint: true }, attempts: 3, trust: false, losses: 1, calls: 1, outcome: 'unknown' },
  { name: 'unsafe side effect', hints: {}, attempts: 3, trust: true, losses: 1, calls: 1, outcome: 'unknown', effects: 1 },
  { name: 'attempt allowance exhausted', hints: { readOnlyHint: true }, attempts: 2, trust: true, losses: 2, calls: 2, outcome: 'unknown' },
  { name: 'later 401 keeps uncertainty', hints: { readOnlyHint: true }, attempts: 3, trust: true, losses: 1, calls: 2, outcome: 'later-unauthorized' },
  { name: 'host cancellation', hints: { readOnlyHint: true }, attempts: 3, trust: true, losses: 0, calls: 1, outcome: 'cancelled' },
  { name: 'completed malformed event', hints: { readOnlyHint: true }, attempts: 3, trust: true, losses: 0, calls: 1, outcome: 'malformed' },
  { name: 'completed protocol error', hints: { readOnlyHint: true }, attempts: 3, trust: true, losses: 0, calls: 1, outcome: 'protocol-error' },
  { name: 'completed tool error', hints: { readOnlyHint: true }, attempts: 3, trust: true, losses: 0, calls: 1, outcome: 'tool-error' },
  { name: 'completed result', hints: { readOnlyHint: true }, attempts: 3, trust: true, losses: 0, calls: 1, outcome: 'complete' },
  { name: 'allowance belongs to each input round', hints: { readOnlyHint: true }, attempts: 2, trust: true, losses: 0, calls: 4, outcome: 'rounds' }
];

// runCase checks mirrored metadata, fresh identities and unchanged round inputs.
// The ten-second deadline protects this case's process and sockets; it is not a
// production request timeout or a limit on the operation's input rounds.
async function runCase(test) {
  const calls = [];
  let effects = 0;
  let cancelled = false;
  let failure;
  let child;
  let timedOut = false;
  const started = performance.now();
  const server = createServer(async (request, response) => {
    try {
      assert.equal(request.method, 'POST');
      assert.equal(request.url, '/mcp');
      assert.equal(request.headers['mcp-protocol-version'], revision);
      assert.equal(request.headers['mcp-session-id'], undefined);
      assert.equal(request.headers['last-event-id'], undefined);
      assert.ok(request.headers.accept.includes('application/json'));
      assert.ok(request.headers.accept.includes('text/event-stream'));
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const message = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      assert.equal(message.jsonrpc, '2.0');
      assert.equal(request.headers['mcp-method'], message.method);
      assert.equal(message.params._meta['io.modelcontextprotocol/protocolVersion'], revision);
      assert.deepEqual(message.params._meta['io.modelcontextprotocol/clientCapabilities'], {});
      assert.deepEqual(message.params._meta['io.modelcontextprotocol/clientInfo'], { name: 'goa-ai-conformance', version: '1' });
      if (message.method === 'tools/list') {
        response.writeHead(200, { 'Content-Type': 'application/json' });
        response.end(JSON.stringify({ jsonrpc: '2.0', id: message.id, result: {
          resultType: 'complete', ttlMs: 0, cacheScope: 'private', tools: [{
            name: 'operation', annotations: test.hints,
            inputSchema: { type: 'object', properties: { operation: { type: 'string' } }, required: ['operation'], additionalProperties: false },
            outputSchema: { type: 'number' }
          }]
        } }));
        return;
      }
      assert.equal(message.method, 'tools/call');
      assert.equal(request.headers['mcp-name'], 'operation');
      assert.equal(message.params.name, 'operation');
      assert.deepEqual(message.params.arguments, { operation: 'same' });
      assert.ok(typeof message.id === 'string' && message.id.length > 0);
      assert.ok(!calls.some(prior => prior.id === message.id));
      calls.push(message);
      assert.ok(calls.length <= test.calls, 'unexpected repeat execution');
      if (test.name === 'unsafe side effect') effects++;
      if (test.name === 'trusted idempotent' && effects === 0) effects++;
      if (test.outcome === 'later-unauthorized' && calls.length === 2) {
        response.writeHead(401, { 'WWW-Authenticate': 'Bearer error="invalid_token"' });
        response.end();
        return;
      }
      response.writeHead(200, { 'Content-Type': 'text/event-stream' });
      if (test.outcome === 'cancelled') {
        response.on('close', () => { cancelled = true; });
        assert.ok(typeof message.params._meta.progressToken === 'string');
        response.write(`data: ${JSON.stringify({ jsonrpc: '2.0', method: 'notifications/progress', params: { progressToken: message.params._meta.progressToken, progress: 0 } })}\n\n`);
        return;
      }
      const lostRound = test.outcome === 'rounds' && calls.length % 2 === 1;
      if (calls.length <= test.losses || lostRound) {
        response.end(': accepted\n\n');
        return;
      }
      if (test.outcome === 'malformed') {
        response.end('data: broken\n\n');
        return;
      }
      if (test.outcome === 'protocol-error') {
        response.end(`data: ${JSON.stringify({ jsonrpc: '2.0', id: message.id, error: { code: -32602, message: 'rejected' } })}\n\n`);
        return;
      }
      const result = test.outcome === 'rounds' && calls.length === 2
        ? { resultType: 'input_required', requestState: 'opaque-sse-round' }
        : { resultType: 'complete', content: [], structuredContent: 42, ...(test.outcome === 'tool-error' ? { isError: true } : {}) };
      response.end(`data: ${JSON.stringify({ jsonrpc: '2.0', id: message.id, result })}\n\n`);
    } catch (error) {
      failure = error;
      response.destroy();
    }
  });
  const deadline = setTimeout(() => {
    timedOut = true;
    if (child && child.exitCode === null) child.kill('SIGTERM');
    server.closeAllConnections();
  }, 10000);
  try {
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', resolve);
    });
    const endpoint = `http://127.0.0.1:${server.address().port}/mcp`;
    child = spawn(driver, [endpoint], {
      stdio: ['ignore', 'ignore', 'inherit'],
      env: { ...process.env, MCP_CONFORMANCE_PROTOCOL_VERSION: revision,
        MCP_CONFORMANCE_SCENARIO: 'interrupted-sse',
        MCP_CONFORMANCE_CONTEXT: JSON.stringify({ outcome: test.outcome, attempts: test.attempts, trust: test.trust }) }
    });
    await new Promise((resolve, reject) => {
      child.once('error', reject);
      child.once('exit', (code, signal) => code === 0 ? resolve() : reject(new Error(`driver exited ${code ?? signal}`)));
    });
    await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
    assert.equal(timedOut, false, 'case exceeded its deadline');
    if (failure) throw failure;
    assert.equal(calls.length, test.calls);
    assert.equal(effects, test.effects ?? 0);
    if (test.outcome === 'cancelled') assert.equal(cancelled, true);
    if (test.outcome === 'rounds') {
      assert.deepEqual(calls[0].params, calls[1].params);
      assert.deepEqual(calls[2].params, calls[3].params);
      assert.equal(calls[0].params.requestState, undefined);
      assert.equal(calls[2].params.requestState, 'opaque-sse-round');
      const { requestState, ...continued } = calls[2].params;
      assert.equal(requestState, 'opaque-sse-round');
      assert.deepEqual(continued, calls[0].params);
    } else {
      for (const call of calls) assert.deepEqual(call.params, calls[0].params);
    }
    console.log(JSON.stringify({ case: test.name, status: 'PASS', attempts: calls.length, effects, durationMs: Math.round(performance.now() - started) }));
  } finally {
    clearTimeout(deadline);
    if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
    server.closeAllConnections();
    if (server.listening) await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
  }
}

for (const test of cases) await runCase(test);
