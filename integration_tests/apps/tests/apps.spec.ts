// Exercise a generated Goa server through the official client and a real
// browser. Negative checks must stop before any forbidden domain side effect.
import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";

test("generated Apps exchange preserves private data and rejects forbidden access", async ({ page }) => {
  page.on("pageerror", (error) => console.error("Browser error:", error.message));
  const forbidden: string[] = [];
  const methods: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("43172/forbidden")) forbidden.push(request.url());
    if (request.url().endsWith("/mcp") && request.method() === "POST") {
      const body = request.postDataJSON() as { method: string; params?: { name?: string } };
      methods.push(body.params?.name ?? body.method);
    }
  });
  await page.goto("/");
  await expect(page.locator("#status")).toHaveText("Connected using modern");
  await page.getByRole("button", { name: "Show record" }).click();
  await expect(page.locator("#status")).toHaveText("App ready");
  const view = page.frameLocator("#panel > iframe").frameLocator("iframe");
  await expect(view.locator("#record")).toHaveText("record-1");
  await view.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(view.locator("#result")).toContainText("refreshed");
  await view.getByRole("button", { name: "Try model-only tool" }).click();
  await expect(view.locator("#result")).toContainText("not callable by this app");
  expect(methods).not.toContain("query");
  expect(methods).not.toContain("initialize");
  await view.getByRole("button", { name: "Try undeclared network" }).click();
  await expect(view.locator("#security")).toHaveText("Network blocked");
  await view.getByRole("button", { name: "Try undeclared frame" }).click();
  await view.getByRole("button", { name: "Try host access" }).click();
  await expect(view.locator("#security")).toHaveText("Host access blocked");
  expect(await page.locator("body").getAttribute("data-escaped")).toBeNull();
  expect(forbidden).toEqual([]);
  await page.getByRole("button", { name: "Close app" }).click();
  await expect(page.locator("#status")).toHaveText("App closed");
  await expect(page.locator("#panel iframe")).toHaveCount(0);
  // A new view gets its own connection to the bridge after the old one closed.
  await page.getByRole("button", { name: "Show record" }).click();
  await expect(page.locator("#status")).toHaveText("App ready");
  await expect(view.locator("#record")).toHaveText("record-1");
  await view.getByRole("button", { name: "Wait for host cancellation" }).click();
  await expect.poll(() => evidence("wait_started")).toBe("started");
  await page.getByRole("button", { name: "Close app" }).click();
  await expect(page.locator("#status")).toHaveText("App closed");
  await expect.poll(() => evidence("wait_canceled")).toBe("canceled");
});

test("a navigated frame cannot send requests or receive private results", async ({ page }) => {
  await page.goto("/origin-probe");
  await expect(page.locator("#status")).toHaveText("Frame loaded");
  const peer = page.frameLocator("iframe");
  await peer.getByRole("button", { name: "Send message to host" }).click();
  await expect(page.locator("#observed")).toHaveText("1");
  await expect(page.locator("#received")).toHaveText("0");
  await page.getByRole("button", { name: "Send private message" }).click();
  await expect(peer.locator("#secret")).toHaveText("No private message");
  await page.getByRole("button", { name: "Navigate to allowed origin" }).click();
  await expect(page.locator("#status")).toHaveText("Frame loaded");
  await peer.getByRole("button", { name: "Send message to host" }).click();
  await expect(page.locator("#observed")).toHaveText("2");
  await expect(page.locator("#received")).toHaveText("1");
  await page.getByRole("button", { name: "Send private message" }).click();
  await expect(peer.locator("#secret")).toHaveText("private record");
  await page.getByRole("button", { name: "Close transport" }).click();
  await expect(page.locator("#status")).toHaveText("Transport closed");
  await peer.getByRole("button", { name: "Send message to host" }).click();
  await expect(page.locator("#observed")).toHaveText("3");
  await expect(page.locator("#received")).toHaveText("1");
});

async function evidence(name: "wait_started" | "wait_canceled"): Promise<string> {
  try {
    return await readFile(`.cache/${name}`, "utf8");
  } catch (error) {
    if (error instanceof Error && "code" in error && error.code === "ENOENT") return "";
    throw error;
  }
}
