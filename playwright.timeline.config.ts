import { defineConfig } from "@playwright/test";
import tasks from "./playwright.tasks.config";
export default defineConfig({
  ...tasks,
  testMatch: "timeline.spec.ts",
  outputDir: "test-results/timeline",
});
