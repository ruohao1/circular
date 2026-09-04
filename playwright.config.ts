import { defineConfig } from "@playwright/test";

process.env.CIRCULAR_E2E_PREFIX ??= `__circular_ui_test_${Date.now()}_`;
const compose = process.env.CIRCULAR_E2E_COMPOSE === "1";
if (compose && !process.env.CIRCULAR_EXECUTION_HOST_ROOT)
  throw new Error(
    "Compose tests require CIRCULAR_EXECUTION_HOST_ROOT for local repository fixtures.",
  );
if (!compose && !process.env.TEST_DATABASE_URL)
  throw new Error(
    "TEST_DATABASE_URL must identify a disposable PostgreSQL database.",
  );

export default defineConfig({
  testDir: "./tests/browser",
  workers: 1,
  timeout: 60_000,
  expect: { timeout: 20_000 },
  use: {
    baseURL: compose ? "http://localhost:5173" : "http://127.0.0.1:15173",
    viewport: { width: 1440, height: 1000 },
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  webServer: compose
    ? []
    : [
        {
          command: "uv run python scripts/e2e_stack.py",
          url: "http://127.0.0.1:18000/api/v1/health",
          timeout: 120_000,
          reuseExistingServer: false,
          gracefulShutdown: { signal: "SIGTERM", timeout: 100_000 },
        },
        {
          command:
            "corepack pnpm --filter @circular/web dev --host 127.0.0.1 --port 15173",
          url: "http://127.0.0.1:15173",
          timeout: 30_000,
          env: { VITE_API_URL: "http://127.0.0.1:18000" },
          reuseExistingServer: false,
        },
      ],
});
