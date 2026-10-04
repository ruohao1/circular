import { defineConfig } from "@playwright/test";

// Documentation is static and must work without an API, database, or worker.
export default defineConfig({
  testDir: "./tests/browser",
  testMatch: "docs.spec.ts",
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL: "http://127.0.0.1:15174",
    viewport: { width: 1440, height: 1000 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command:
      "corepack pnpm --filter @circular/web dev --host 127.0.0.1 --port 15174 --strictPort",
    url: "http://127.0.0.1:15174",
    timeout: 30_000,
    env: { VITE_API_URL: "http://127.0.0.1:9" },
    reuseExistingServer: false,
  },
});
