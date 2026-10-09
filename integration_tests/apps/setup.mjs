// Build one generated Goa peer and three browser bundles before acceptance.
// Generated files and binaries remain local to this example.
import { mkdir, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { build } from "esbuild";

function run(command, args, cwd) {
  const started = performance.now();
  const result = spawnSync(command, args, {
    cwd,
    stdio: "inherit",
    env: { ...process.env, GOWORK: "off", GOFLAGS: "-mod=mod" },
  });
  if (result.error) throw result.error;
  if (result.status !== 0) throw new Error(`${command} exited with ${result.status}`);
  console.info(`${command} ${args[0]}: ${((performance.now() - started) / 1000).toFixed(2)}s`);
}

await mkdir(".cache", { recursive: true });
await build({ entryPoints: ["src/host.ts", "src/sandbox.ts", "src/origin_probe.ts"], bundle: true, format: "esm", outdir: ".cache" });
const app = await build({ entryPoints: ["src/panel.ts"], bundle: true, format: "esm", write: false });
await writeFile(".cache/panel.html", `<!doctype html><html><head><meta charset="utf-8"><title>Record panel</title></head><body>
<h1>Record panel</h1><p id="record">Waiting for record</p><p id="result"></p>
<button id="refresh">Refresh</button><button id="query">Try model-only tool</button>
<button id="wait">Wait for host cancellation</button>
<button id="network">Try undeclared network</button><button id="frame">Try undeclared frame</button>
<button id="escape">Try host access</button><p id="security"></p>
<script type="module">${app.outputFiles[0].text.replaceAll("</script", "<\\/script")}</script></body></html>`);
run("go", ["run", "goa.design/goa/v3/cmd/goa", "gen", "apps-peer.local/design"], "fixture");
run("go", ["mod", "tidy"], "fixture");
run("go", ["build", "-o", "../.cache/peer", "."], "fixture");
