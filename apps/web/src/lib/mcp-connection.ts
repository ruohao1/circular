export type MCPRuntime = "docker" | "binary";

// Copyable commands are POSIX shell commands. Every configurable value is
// quoted; the JSON alternative passes arguments directly to the MCP client.
const quote = (value: string) => `'${value.replaceAll("'", "'\\''")}'`;

export function httpMCPConnection(apiURL: string, readOnly: boolean) {
  const base = new URL(apiURL);
  if (
    !["http:", "https:"].includes(base.protocol) ||
    base.username ||
    base.password
  ) {
    throw new Error(
      "Circular's API URL must be an HTTP or HTTPS address without credentials.",
    );
  }
  const url = new URL(readOnly ? "/mcp/read-only" : "/mcp", base.origin).href;
  return { url, codex: `codex mcp add circular --url ${quote(url)}` };
}

export function mcpConnection({
  folder,
  runtime,
  readOnly,
  apiURL,
  webURL,
}: {
  folder: string;
  runtime: MCPRuntime;
  readOnly: boolean;
  apiURL: string;
  webURL: string;
}) {
  const root = folder.trim().replace(/\/+$/, "");
  if (!root.startsWith("/") || /[\r\n\0]/.test(root)) return null;
  const binary = `${root}/dist/circular-mcp`;
  const compose = ["compose", "-f", `${root}/compose.yaml`, "--profile", "mcp"];
  const serverArgs = [
    "--api-url",
    runtime === "docker" ? "http://api:8000" : apiURL,
    "--web-url",
    webURL,
    ...(readOnly ? ["--read-only"] : []),
  ];
  const command = runtime === "docker" ? "docker" : binary;
  const args =
    runtime === "docker"
      ? [...compose, "run", "--rm", "--no-deps", "-T", "mcp", ...serverArgs]
      : serverArgs;
  const invocation = [command, ...args].map(quote).join(" ");
  return {
    install:
      runtime === "docker"
        ? ["docker", ...compose, "build", "mcp"].map(quote).join(" ")
        : `mkdir -p ${quote(`${root}/dist`)}\ngo -C ${quote(root)} build -o ${quote(binary)} ./cmd/circular-mcp`,
    codex: `codex mcp add circular -- ${invocation}`,
    json: JSON.stringify(
      { mcpServers: { circular: { command, args } } },
      null,
      2,
    ),
    check: `${invocation} --check`,
  };
}
