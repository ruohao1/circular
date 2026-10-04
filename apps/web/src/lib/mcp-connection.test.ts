import { describe, expect, it } from "vitest";
import { httpMCPConnection, mcpConnection } from "./mcp-connection";

describe("MCP URL connection", () => {
  it("uses the API origin without its REST path", () => {
    expect(httpMCPConnection("http://localhost:8000/api/v1", false)).toEqual({
      url: "http://localhost:8000/mcp",
      codex: "codex mcp add circular --url 'http://localhost:8000/mcp'",
    });
  });

  it("selects the read-only endpoint and preserves a configured host and port", () => {
    expect(
      httpMCPConnection("https://circular.example:8443/api/v1/", true),
    ).toEqual({
      url: "https://circular.example:8443/mcp/read-only",
      codex:
        "codex mcp add circular --url 'https://circular.example:8443/mcp/read-only'",
    });
  });

  it("drops query and fragment data and quotes IPv6 addresses for the shell", () => {
    expect(
      httpMCPConnection("http://[::1]:8000/api/v1?old=true#ignored", false)
        .codex,
    ).toBe("codex mcp add circular --url 'http://[::1]:8000/mcp'");
  });

  it("keeps a quote in the configured URL inside a single shell argument", () => {
    expect(
      httpMCPConnection("http://circular'example.test/api/v1", false).codex,
    ).toBe(
      "codex mcp add circular --url 'http://circular'\\''example.test/mcp'",
    );
  });

  it("rejects credentials and non-HTTP addresses", () => {
    for (const url of [
      "file:///tmp/circular",
      "https://user:password@example.com",
    ]) {
      expect(() => httpMCPConnection(url, false)).toThrow();
    }
  });
});

const defaults = {
  folder: "/work/circular",
  runtime: "docker" as const,
  readOnly: false,
  apiURL: "http://localhost:8000",
  webURL: "http://localhost:5173",
};

describe("MCP connection configuration", () => {
  it("keeps container and host URLs separate and passes read-only to the server", () => {
    const result = mcpConnection({ ...defaults, readOnly: true })!;
    const config = JSON.parse(result.json).mcpServers.circular;
    expect(config.command).toBe("docker");
    expect(config.args).toEqual([
      "compose",
      "-f",
      "/work/circular/compose.yaml",
      "--profile",
      "mcp",
      "run",
      "--rm",
      "--no-deps",
      "-T",
      "mcp",
      "--api-url",
      "http://api:8000",
      "--web-url",
      "http://localhost:5173",
      "--read-only",
    ]);
    expect(result.codex).toContain("codex mcp add circular --");
    expect(result.check).toContain("--check");
    expect(result.install).not.toContain("--read-only");
  });

  it("preserves paths containing spaces, quotes and shell substitutions as data", () => {
    const folder = "/work/O'Brien $(touch never) `echo literal`/";
    const result = mcpConnection({ ...defaults, folder, runtime: "binary" })!;
    const config = JSON.parse(result.json).mcpServers.circular;
    expect(config.command).toBe(
      "/work/O'Brien $(touch never) `echo literal`/dist/circular-mcp",
    );
    expect(config.args).toContain("http://localhost:8000");
    expect(config.args).not.toContain("--read-only");
    expect(result.codex).toContain(
      "'/work/O'\\''Brien $(touch never) `echo literal`/dist/circular-mcp'",
    );
    expect(result.install).toContain(
      "go -C '/work/O'\\''Brien $(touch never) `echo literal`'",
    );
  });

  it("requires an absolute single-line repository path", () => {
    for (const folder of [
      "",
      ".",
      "~/circular",
      "/",
      "/work\nextra",
      "/work\0extra",
    ]) {
      expect(mcpConnection({ ...defaults, folder })).toBeNull();
    }
  });
});
