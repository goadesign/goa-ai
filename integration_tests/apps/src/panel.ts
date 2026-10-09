// The panel uses the official Apps SDK. The host receives every tool request
// and decides whether it can reach the associated Goa service.
import { App } from "@modelcontextprotocol/ext-apps";

const app = new App({ name: "record-panel", version: "1" }, {});
const result = document.querySelector<HTMLParagraphElement>("#result")!;
const security = document.querySelector<HTMLParagraphElement>("#security")!;
app.ontoolresult = (response) => {
  const id = response._meta?.recordId;
  if (typeof id !== "string") throw new Error("The record result is missing its private identity");
  document.querySelector<HTMLParagraphElement>("#record")!.textContent = id;
};
app.onteardown = async () => {
  document.body.dataset.tornDown = "true";
  return {};
};
await app.connect();
document.querySelector<HTMLButtonElement>("#refresh")!.onclick = () => call("refresh");
document.querySelector<HTMLButtonElement>("#query")!.onclick = () => call("query");
document.querySelector<HTMLButtonElement>("#wait")!.onclick = () => call("wait");
document.querySelector<HTMLButtonElement>("#network")!.onclick = () => {
  fetch("http://127.0.0.1:43172/forbidden").then(
    () => { security.textContent = "Network unexpectedly allowed"; },
    () => { security.textContent = "Network blocked"; },
  );
};
document.querySelector<HTMLButtonElement>("#frame")!.onclick = () => {
  const frame = document.createElement("iframe");
  frame.src = "http://127.0.0.1:43172/forbidden-frame";
  document.body.append(frame);
};
document.querySelector<HTMLButtonElement>("#escape")!.onclick = () => {
  try {
    window.top!.document.body.dataset.escaped = "true";
    security.textContent = "Host unexpectedly accessible";
  } catch {
    security.textContent = "Host access blocked";
  }
};

function call(name: string): void {
  app.callServerTool({ name, arguments: {} }).then(
    (response) => { result.textContent = name + ": " + JSON.stringify(response.structuredContent); },
    (error: unknown) => { result.textContent = error instanceof Error ? error.message : "Tool rejected"; },
  );
}
