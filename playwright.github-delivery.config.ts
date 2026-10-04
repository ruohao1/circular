import { defineConfig } from "@playwright/test";

// Publishing UI states are exercised without a live GitHub connection or worker.
export default defineConfig({
  testDir: "./tests/browser",
  testMatch: "github-delivery.spec.ts",
  outputDir: "/tmp/circular-github-delivery-browser-results",
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  use: {
    baseURL: "http://127.0.0.1:15175",
    viewport: { width: 1440, height: 1000 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: {
    command:
      "corepack pnpm --filter @circular/web dev --host 127.0.0.1 --port 15175 --strictPort",
    url: "http://127.0.0.1:15175",
    timeout: 30_000,
    env: { VITE_API_URL: "http://127.0.0.1:9" },
    reuseExistingServer: false,
  },
});
