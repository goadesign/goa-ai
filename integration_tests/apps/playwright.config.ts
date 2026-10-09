import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  workers: 1,
  use: { baseURL: "http://127.0.0.1:43170", browserName: "chromium" },
  webServer: { command: "node server.mjs", url: "http://127.0.0.1:43170", reuseExistingServer: false },
});
