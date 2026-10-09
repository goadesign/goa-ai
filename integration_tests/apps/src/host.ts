// This host composes the official MCP client and Apps bridge with generated
// Goa endpoints. A view can call only app-visible tools on this one connection.
import { Client, StreamableHTTPClientTransport, ProtocolError, ProtocolErrorCode, type Tool } from "@modelcontextprotocol/client";
import { AppBridge, RESOURCE_MIME_TYPE } from "@modelcontextprotocol/ext-apps/app-bridge";
import { McpUiToolMetaSchema, McpUiResourceMetaSchema } from "@modelcontextprotocol/ext-apps";
import { OriginTransport } from "./origin_transport.js";

const status = document.querySelector<HTMLParagraphElement>("#status")!;
const container = document.querySelector<HTMLDivElement>("#panel")!;
const showButton = document.querySelector<HTMLButtonElement>("#show")!;
const closeButton = document.querySelector<HTMLButtonElement>("#close")!;
const sandboxURL = new URL(document.body.dataset.sandbox!);
if (sandboxURL.origin === window.location.origin) {
  throw new Error("The app sandbox must use a different origin from the host");
}
const client = new Client({ name: "record-host", version: "1" }, {
  versionNegotiation: { mode: { pin: "2026-07-28" } },
  capabilities: { extensions: { "io.modelcontextprotocol/ui": { mimeTypes: [RESOURCE_MIME_TYPE] } } },
});
try {
  await client.connect(new StreamableHTTPClientTransport(new URL("/mcp", window.location.href)));
} catch (error) {
  reportError(error);
  throw error;
}
let closeView: (() => Promise<void>) | undefined;

showButton.onclick = () => {
  showButton.disabled = true;
  closeButton.disabled = true;
  show().catch(failView);
};
closeButton.onclick = () => {
  showButton.disabled = true;
  closeButton.disabled = true;
  close().then(() => { showButton.disabled = false; }, reportError);
};
showButton.disabled = false;
status.textContent = "Connected using " + client.getProtocolEra();

async function show(): Promise<void> {
  await close();
  const catalog = await client.listTools(undefined, { cacheMode: "refresh" });
  const selected = catalog.tools.find((tool) => tool.name === "show");
  if (!selected) throw new Error("The record tool is missing");
  const ui = toolUI(selected);
  if (!ui.resourceUri?.startsWith("ui://")) throw new Error("The tool has no UI resource");
  const resource = await client.readResource({ uri: ui.resourceUri }, { cacheMode: "refresh" });
  if (resource.contents.length !== 1) throw new Error("The view must have exactly one HTML document");
  const html = resource.contents[0]!;
  if (html.uri !== ui.resourceUri || html.mimeType !== RESOURCE_MIME_TYPE) {
    throw new Error("The server returned a different resource or MIME type");
  }
  const metadata = html._meta?.ui === undefined ? {} : McpUiResourceMetaSchema.parse(html._meta.ui);
  // This host denies all external origins and browser permissions. Its HTTP
  // sandbox policy enforces that choice even if an app requests broader access.
  if (metadata.domain !== undefined) throw new Error("This host does not assign app-selected origins");
  const documentHTML = "text" in html ? html.text : new TextDecoder("utf-8", { fatal: true }).decode(
    Uint8Array.from(atob(html.blob), (character) => character.charCodeAt(0)),
  );
  const frame = document.createElement("iframe");
  frame.title = "Isolated MCP record app";
  frame.setAttribute("sandbox", "allow-scripts allow-same-origin");
  frame.setAttribute("allow", "camera 'none'; microphone 'none'; geolocation 'none'; clipboard-write 'none'");
  frame.style.cssText = "width:100%;height:400px;border:1px solid #aaa";
  container.append(frame);
  const controller = new AbortController();
  let closing = false;
  let initialized = false;
  const bridge = new AppBridge(null, { name: "record-host", version: "1" }, { serverTools: {} }, {
    hostContext: {
      toolInfo: { tool: selected },
      displayMode: "inline",
      availableDisplayModes: ["inline"],
      containerDimensions: { width: container.clientWidth, height: 400 },
      sandbox: { csp: {}, permissions: {} },
    },
  });
  bridge.oncalltool = async (params, extra) => {
    if (closing) throw new ProtocolError(ProtocolErrorCode.InvalidRequest, "The view is closing");
    const signal = AbortSignal.any([controller.signal, extra.mcpReq.signal]);
    // Read with this connection's credentials; a view cannot select a server,
    // change authorization, or reuse permissions from an earlier catalog.
    const current = await client.listTools(undefined, { signal, cacheMode: "refresh" });
    const tool = current.tools.find((entry) => entry.name === params.name);
    if (!tool) throw new ProtocolError(ProtocolErrorCode.InvalidParams, "The tool is not callable by this app");
    const visibility = toolUI(tool).visibility;
    if (visibility !== undefined && !visibility.includes("app")) {
      throw new ProtocolError(ProtocolErrorCode.InvalidParams, "The tool is not callable by this app");
    }
    return client.callTool(params, { signal });
  };
  bridge.addEventListener("sandboxready", () => {
    bridge.sendSandboxResourceReady({ html: documentHTML, sandbox: "allow-scripts", permissions: {}, csp: {} }).catch(failView);
  });
  bridge.addEventListener("initialized", () => {
    initialized = true;
    deliverResult().catch(failView);
  });
  bridge.addEventListener("sizechange", ({ height }) => {
    if (height !== undefined) frame.style.height = `${height}px`;
  });
  closeView = async () => {
    closing = true;
    controller.abort();
    try {
      if (initialized) await bridge.teardownResource({});
    } finally {
      try {
        await bridge.close();
      } finally {
        frame.remove();
        closeButton.disabled = true;
        status.textContent = "App closed";
      }
    }
  };
  await bridge.connect(new OriginTransport(frame.contentWindow!, sandboxURL.origin));
  closeButton.disabled = false;
  frame.src = sandboxURL.href;
  status.textContent = "Loading isolated app";

  async function deliverResult(): Promise<void> {
    await bridge.sendToolInput({ arguments: {} });
    try {
      const result = await client.callTool({ name: selected!.name, arguments: {} }, { signal: controller.signal });
      await bridge.sendToolResult(result);
      status.textContent = "App ready";
      showButton.disabled = false;
    } catch (error) {
      if (controller.signal.aborted) return;
      await bridge.sendToolCancelled({ reason: error instanceof Error ? error.message : "Tool failed" });
      throw error;
    }
  }
}

// Parse only the current nested UI contract. Deprecated flat keys are ignored.
function toolUI(tool: Tool) {
  const ui = tool._meta?.ui === undefined ? {} : McpUiToolMetaSchema.parse(tool._meta.ui);
  if (ui.visibility !== undefined &&
    (ui.visibility.length === 0 || new Set(ui.visibility).size !== ui.visibility.length)) {
    throw new Error("Invalid tool visibility");
  }
  return ui;
}

async function close(): Promise<void> {
  const dispose = closeView;
  closeView = undefined;
  if (dispose) await dispose();
}

// A failed resource, setup step, or initial call closes this view before the
// host displays the error. The server connection remains owned by the host.
async function failView(error: unknown): Promise<void> {
  try {
    await close();
  } catch (cleanupError) {
    reportError(new AggregateError([error, cleanupError], "App setup and cleanup failed"));
    return;
  } finally {
    showButton.disabled = false;
  }
  reportError(error);
}

function reportError(error: unknown): void {
  status.textContent = error instanceof Error ? error.message : "App operation failed";
}
