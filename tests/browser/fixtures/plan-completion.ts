import type { Page } from "@playwright/test";
import { reviewCurrentProposal } from "./approval";

export async function chooseDirectCompletion(page: Page) {
  await reviewCurrentProposal(page);
}
