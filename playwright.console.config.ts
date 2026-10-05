import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/browser",
  testMatch: "console.spec.ts",
  outputDir: "test-results/console",
  workers: 1,
  timeout: 30_000,
  expect: { timeout: 5_000 },
  use: {
    baseURL: "http://127.0.0.1:15177",
    viewport: { width: 1440, height: 1000 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command:
      "node apps/web/node_modules/vite/bin/vite.js apps/web --host 127.0.0.1 --port 15177 --strictPort",
    url: "http://127.0.0.1:15177",
    timeout: 30_000,
    env: { VITE_API_URL: "http://127.0.0.1:9" },
    reuseExistingServer: false,
  },
});
