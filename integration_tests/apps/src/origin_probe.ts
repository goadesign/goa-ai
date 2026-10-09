// This acceptance page keeps one frame window while navigating between two
// origins. Only the configured origin may receive or send transport messages.
import { OriginTransport } from "./origin_transport.js";

const frame = document.querySelector<HTMLIFrameElement>("iframe")!;
const received = document.querySelector<HTMLParagraphElement>("#received")!;
const status = document.querySelector<HTMLParagraphElement>("#status")!;
const observed = document.querySelector<HTMLParagraphElement>("#observed")!;
let observations = 0;
// The producer posts this test marker after each request. Seeing it proves the
// preceding browser message was dispatched before a negative assertion runs.
window.addEventListener("message", (event: MessageEvent<unknown>) => {
  if (event.source === frame.contentWindow && typeof event.data === "object" &&
    event.data !== null && "type" in event.data && event.data.type === "probe-barrier") {
    observed.textContent = String(++observations);
  }
}, true);
const transport = new OriginTransport(frame.contentWindow!, "http://127.0.0.1:43171");
let count = 0;
transport.onmessage = () => { received.textContent = String(++count); };
await transport.start();
frame.onload = () => { status.textContent = "Frame loaded"; };
frame.src = "http://127.0.0.1:43172/probe";
document.querySelector<HTMLButtonElement>("#send")!.onclick = () => {
  transport.send({ jsonrpc: "2.0", method: "probe/private", params: { secret: "private record" } }).catch(reportError);
};
document.querySelector<HTMLButtonElement>("#navigate")!.onclick = () => {
  status.textContent = "Navigating";
  frame.src = "http://127.0.0.1:43171/probe";
};
document.querySelector<HTMLButtonElement>("#close")!.onclick = () => {
  transport.close().then(() => { status.textContent = "Transport closed"; }, reportError);
};

function reportError(error: unknown): void {
  status.textContent = error instanceof Error ? error.message : "Transport failed";
}
