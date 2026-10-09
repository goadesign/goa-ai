// The host serves this proxy from its own isolated origin with a restrictive
// HTTP content policy. It relays only messages from its exact parent and view.
import { McpUiSandboxResourceReadyNotificationSchema } from "@modelcontextprotocol/ext-apps";

const parentOrigin = new URL(document.referrer).origin;
if (window.parent === window || parentOrigin === window.location.origin) {
  throw new Error("The sandbox must be embedded by a different origin");
}
const inner = document.createElement("iframe");
inner.title = "Record panel";
inner.setAttribute("sandbox", "allow-scripts");
inner.setAttribute("allow", "camera 'none'; microphone 'none'; geolocation 'none'; clipboard-write 'none'");
inner.style.cssText = "width:100%;height:100%;border:0";
document.body.append(inner);
let loaded = false;

window.addEventListener("message", (event: MessageEvent<unknown>) => {
  if (event.source === window.parent && event.origin === parentOrigin) {
    const ready = McpUiSandboxResourceReadyNotificationSchema.safeParse(event.data);
    if (ready.success) {
      if (loaded) throw new Error("The host already loaded this view");
      loaded = true;
      inner.srcdoc = ready.data.params.html;
      return;
    }
    if (isSandboxControl(event.data)) return;
    inner.contentWindow!.postMessage(event.data, "*");
  } else if (event.source === inner.contentWindow && event.origin === "null") {
    if (isSandboxControl(event.data)) return;
    window.parent.postMessage(event.data, parentOrigin);
  }
});

window.parent.postMessage({ jsonrpc: "2.0", method: "ui/notifications/sandbox-proxy-ready", params: {} }, parentOrigin);

// Messages from a view cannot replace its document or impersonate proxy setup.
function isSandboxControl(value: unknown): boolean {
  return typeof value === "object" && value !== null && "method" in value &&
    typeof value.method === "string" && value.method.startsWith("ui/notifications/sandbox-");
}
