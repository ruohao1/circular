import { defineConfig } from "@playwright/test";
import tasks from "./playwright.tasks.config";
export default defineConfig({
  ...tasks,
  testMatch: "task-proposals.spec.ts",
  outputDir: "test-results/proposals",
  use: { ...tasks.use, baseURL: "http://127.0.0.1:15184" },
  webServer: {
    command:
      "node apps/web/node_modules/vite/bin/vite.js apps/web --host 127.0.0.1 --port 15184 --strictPort",
    url: "http://127.0.0.1:15184",
    timeout: 30_000,
    env: { VITE_API_URL: "http://127.0.0.1:9" },
    reuseExistingServer: false,
  },
});
