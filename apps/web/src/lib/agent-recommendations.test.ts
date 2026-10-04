import { describe, expect, it } from "vitest";
import { agentRecommendations } from "./agent-recommendations";

const role = [
  "### Product engineer",
  "",
  "**Purpose:** Build the console.",
  "",
  "**When to use:** UI tasks.",
  "",
  "Ready-to-copy instructions:",
  "",
  "```text",
  "Read AGENTS.md.\nRun relevant checks.",
  "```",
].join("\n");

describe("discovery report recommendations", () => {
  it("extracts only complete roles inside the recommendation section", () => {
    const result = agentRecommendations(
      [
        "# Discovery",
        "## 4. Recommended agents",
        role,
        "## 5. Open questions",
        role.replace("Product engineer", "Not a recommendation"),
      ].join("\n\n"),
    );
    expect(result).toEqual([
      {
        name: "Product engineer",
        purpose: "Purpose: Build the console.\n\nWhen to use: UI tasks.",
        instructions: "Read AGENTS.md.\nRun relevant checks.",
      },
    ]);
  });
  it("does not turn ordinary code blocks, partial suggestions or unrelated headings into agents", () => {
    expect(agentRecommendations(role)).toEqual([]);
    expect(
      agentRecommendations(
        "## Recommended agents\n### No instructions\n**Purpose:** Improve UI.",
      ),
    ).toEqual([]);
    expect(
      agentRecommendations(
        "## Recommended agents\n### No purpose\n```text\nBuild this\n```",
      ),
    ).toEqual([]);
    expect(
      agentRecommendations(
        "## Recommended agents\n### SQL sample\n**Purpose:** Sample only\n```sql\nSELECT 1;\n```",
      ),
    ).toEqual([]);
  });
  it("deduplicates repeated recommendations and ignores oversized instructions", () => {
    expect(
      agentRecommendations(["## Recommended agents", role, role].join("\n")),
    ).toHaveLength(1);
    expect(
      agentRecommendations(
        [
          "## Recommended agents",
          role.replace(
            "Read AGENTS.md.\nRun relevant checks.",
            "a".repeat(20001),
          ),
        ].join("\n"),
      ),
    ).toEqual([]);
  });
});
