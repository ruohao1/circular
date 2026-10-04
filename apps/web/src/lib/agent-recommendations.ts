import { unified } from "unified";
import remarkParse from "remark-parse";
import { toString } from "mdast-util-to-string";
import type { AgentProposalCreate } from "@/api";

const parser = unified().use(remarkParse);

// Older discovery reports have no structured tool calls. Only recognize the
// documented recommendation section with a named role and fenced instructions.
export function agentRecommendations(markdown: string): AgentProposalCreate[] {
  const nodes = parser.parse(markdown).children;
  const recommendations: AgentProposalCreate[] = [];
  let section = 0;
  let role:
    | { depth: number; name: string; purpose: string[]; instructions: string[] }
    | undefined;
  function finish() {
    if (!role) return;
    const purpose = role.purpose.join("\n\n").trim();
    const instructions = role.instructions.join("\n\n").trim();
    if (
      role.name &&
      role.name.length <= 200 &&
      purpose &&
      purpose.length <= 2000 &&
      instructions &&
      instructions.length <= 20000 &&
      recommendations.length < 12 &&
      !recommendations.some((item) => item.name === role!.name)
    ) {
      recommendations.push({ name: role.name, purpose, instructions });
    }
    role = undefined;
  }
  for (const node of nodes) {
    if (node.type === "heading") {
      if (section && node.depth <= section) {
        finish();
        section = 0;
      }
      if (
        /^(?:\d+[.)]\s*)?(?:recommended|suggested) agents\s*:?$/i.test(
          toString(node).trim(),
        )
      ) {
        section = node.depth;
        continue;
      }
      if (section && node.depth === section + 1) {
        finish();
        role = {
          depth: node.depth,
          name: toString(node).trim(),
          purpose: [],
          instructions: [],
        };
      }
      continue;
    }
    if (!section || !role) continue;
    if (
      node.type === "code" &&
      (!node.lang || /^(?:text|plaintext|markdown|md)$/i.test(node.lang))
    ) {
      role.instructions.push(node.value);
    } else if (node.type === "paragraph" && role.instructions.length === 0) {
      const text = toString(node).trim();
      if (
        text &&
        !/^(?:ready.to.copy(?:\s+instructions)?|(?:agent\s+)?instructions)\s*:?$/i.test(
          text,
        )
      )
        role.purpose.push(text);
    }
  }
  finish();
  return recommendations;
}
