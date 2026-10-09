// Serve the host and sandbox on different origins. The MCP endpoint is a byte
// proxy to the generated Goa peer; this server defines no second JSON contract.
import { createServer, request } from "node:http";
import { readFile, rm } from "node:fs/promises";
import { pipeline } from "node:stream/promises";
import { spawn } from "node:child_process";

const hostOrigin = "http://127.0.0.1:43170";
const sandboxOrigin = "http://127.0.0.1:43171";
// Remove only this test's two prior evidence files before starting its peer.
await rm(".cache/wait_started", { force: true });
await rm(".cache/wait_canceled", { force: true });
const peer = spawn("./.cache/peer", ["-panel", ".cache/panel.html", "-evidence", ".cache"], { stdio: "inherit" });
peer.on("error", (error) => { throw error; });
peer.on("exit", (code, signal) => {
  if (code !== 0 && signal !== "SIGINT") process.exitCode = 1;
});
const hostScript = await readFile(".cache/host.js");
const probeScript = await readFile(".cache/origin_probe.js");
const sandboxScript = await readFile(".cache/sandbox.js", "utf8");
const sandboxPolicy = [
  "default-src 'none'",
  "script-src 'unsafe-inline'",
  "style-src 'unsafe-inline'",
  "connect-src 'none'",
  "img-src data:",
  "media-src data:",
  "font-src 'none'",
  "frame-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  `frame-ancestors ${hostOrigin}`,
].join("; ");

const host = createServer(async (incoming, outgoing) => {
  try {
    if (incoming.url === "/mcp") {
      const upstream = request("http://127.0.0.1:43173/mcp", {
        method: incoming.method,
        headers: { ...incoming.headers, host: "127.0.0.1:43173" },
      }, (response) => {
        outgoing.writeHead(response.statusCode, response.headers);
        pipeline(response, outgoing).catch(() => outgoing.destroy());
      });
      upstream.on("error", () => { outgoing.writeHead(502); outgoing.end(); });
      incoming.on("aborted", () => upstream.destroy());
      // A canceled browser response must close the peer's request as well,
      // so its generated Goa endpoint observes context cancellation.
      outgoing.once("close", () => {
        if (!outgoing.writableFinished) upstream.destroy();
      });
      await pipeline(incoming, upstream);
    } else if (incoming.url === "/host.js") {
      outgoing.writeHead(200, { "content-type": "text/javascript" });
      outgoing.end(hostScript);
    } else if (incoming.url === "/origin_probe.js") {
      outgoing.writeHead(200, { "content-type": "text/javascript" });
      outgoing.end(probeScript);
    } else if (incoming.url === "/origin-probe") {
      outgoing.writeHead(200, { "content-type": "text/html" });
      outgoing.end(`<!doctype html><title>Origin transport acceptance</title><iframe title="Origin probe"></iframe>
<p id="received">0</p><p id="status">Starting</p><button id="send">Send private message</button>
<p id="observed">0</p>
<button id="navigate">Navigate to allowed origin</button><button id="close">Close transport</button>
<script type="module" src="/origin_probe.js"></script>`);
    } else if (incoming.url === "/") {
      outgoing.writeHead(200, {
        "content-type": "text/html",
        "content-security-policy": `default-src 'none'; script-src 'self'; connect-src 'self'; frame-src ${sandboxOrigin}; base-uri 'none'; frame-ancestors 'none'`,
        "referrer-policy": "strict-origin",
      });
      outgoing.end(`<!doctype html><html><head><title>MCP Apps host</title></head><body data-sandbox="${sandboxOrigin}/sandbox">
<h1>MCP record app</h1><p>The record panel runs in an isolated frame.</p>
<button id="show" disabled>Show record</button><button id="close" disabled>Close app</button>
<p id="status" role="status">Connecting</p><div id="panel"></div><script type="module" src="/host.js"></script></body></html>`);
    } else {
      outgoing.writeHead(404); outgoing.end();
    }
  } catch {
    outgoing.destroy();
  }
});

const sandbox = createServer((incoming, outgoing) => {
  if (incoming.url === "/probe") { serveProbe(outgoing); return; }
  if (incoming.url !== "/sandbox") { outgoing.writeHead(404); outgoing.end(); return; }
  outgoing.writeHead(200, {
    "content-type": "text/html",
    "content-security-policy": sandboxPolicy,
    "permissions-policy": "camera=(), microphone=(), geolocation=(), clipboard-write=()",
    "referrer-policy": "no-referrer",
    "x-content-type-options": "nosniff",
  });
  outgoing.end(`<!doctype html><html><head><title>MCP app sandbox</title></head><body><script type="module">${sandboxScript.replaceAll("</script", "<\\/script")}</script></body></html>`);
});

const foreign = createServer((incoming, outgoing) => {
  if (incoming.url === "/probe") { serveProbe(outgoing); return; }
  outgoing.writeHead(200, { "content-type": "text/html" });
  outgoing.end("<!doctype html><p>Foreign origin</p>");
});

// Both test origins serve the same message producer, isolating the origin check
// from differences in content. The transport must accept only one of them.
function serveProbe(response) {
  response.writeHead(200, { "content-type": "text/html" });
  response.end(`<!doctype html><title>Message producer</title><p id="secret">No private message</p>
<button id="ping">Send message to host</button><script>
window.addEventListener("message", (event) => {
  if (event.data.method === "probe/private") document.querySelector("#secret").textContent = event.data.params.secret;
});
document.querySelector("#ping").onclick = () => {
  window.parent.postMessage({jsonrpc:"2.0",method:"probe/incoming",params:{}}, ${JSON.stringify(hostOrigin)});
  window.parent.postMessage({type:"probe-barrier"}, ${JSON.stringify(hostOrigin)});
};
</script>`);
}

for (const [server, port] of [[host, 43170], [sandbox, 43171], [foreign, 43172]]) {
  await new Promise((resolve, reject) => { server.once("error", reject); server.listen(port, "127.0.0.1", resolve); });
}

// A test shutdown closes exact listeners and the one child process it started.
async function stop() {
  for (const server of [host, sandbox, foreign]) {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
  peer.kill("SIGINT");
}
process.once("SIGINT", () => { stop().catch(() => { process.exitCode = 1; }); });
process.once("SIGTERM", () => { stop().catch(() => { process.exitCode = 1; }); });
