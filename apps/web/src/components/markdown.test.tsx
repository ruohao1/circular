import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Markdown } from "./markdown";

describe("agent Markdown", () => {
  it("renders headings, GFM tables and highlighted code with a copy control", () => {
    const markup = renderToStaticMarkup(
      <Markdown>
        {[
          "# Report",
          "",
          "| Check | Result |",
          "| --- | --- |",
          "| Types | Pass |",
          "",
          "- [x] Verified",
          "",
          "```js",
          "const value = 2;",
          "```",
          "",
          "```unknown-language",
          "still readable",
          "```",
        ].join("\n")}
      </Markdown>,
    );
    expect(markup).toContain("<h1>Report</h1>");
    expect(markup).toContain("<th>Check</th>");
    expect(markup).toContain("<td>Pass</td>");
    expect(markup).toContain("hljs-keyword");
    expect(markup).toContain("Copy code");
    expect(markup).toContain('type="checkbox"');
    expect(markup).toContain("still readable");
  });
  it("does not execute HTML, unsafe links, or remote image requests", () => {
    const markup = renderToStaticMarkup(
      <Markdown>
        {
          "<script>alert(1)</script>\n\n[unsafe](javascript:alert%281%29)\n\n![remote](https://tracker.invalid/pixel)\n\n[Docs](https://example.test/docs)\n\n[File](/workspace/main.go:12)"
        }
      </Markdown>,
    );
    expect(markup).not.toContain("<script");
    expect(markup).not.toContain("javascript:");
    expect(markup).not.toContain("<img");
    expect(markup).not.toContain("tracker.invalid");
    expect(markup).toContain('href="https://example.test/docs"');
    expect(markup).toContain('rel="noopener noreferrer"');
    expect(markup).toContain('title="/workspace/main.go:12"');
    expect(markup).not.toContain('href="/workspace');
  });
});
