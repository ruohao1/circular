import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { CheckCircle2, LoaderCircle, Plug, Workflow } from "lucide-react";
import { useState } from "react";
import { api, apiUrl } from "@/api";
import { CopyText } from "@/components/markdown";
import { ResourceSelect } from "@/components/resource-select";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  httpMCPConnection,
  mcpConnection,
  type MCPRuntime,
} from "@/lib/mcp-connection";

function Snippet({ title, code }: { title: string; code: string }) {
  return (
    <div className="min-w-0 overflow-hidden rounded-lg border bg-background/70">
      <div className="flex items-center justify-between gap-2 border-b px-3 py-1.5">
        <span className="text-xs font-medium text-muted-foreground">
          {title}
        </span>
        <CopyText
          key={code}
          text={code}
          label={`Copy ${title.toLowerCase()}`}
        />
      </div>
      <pre
        className="max-h-96 overflow-auto whitespace-pre-wrap p-4 font-mono text-xs leading-relaxed [overflow-wrap:anywhere]"
        aria-label={title}
      >
        <code>{code}</code>
      </pre>
    </div>
  );
}

function LocalConnection({ readOnly }: { readOnly: boolean }) {
  const [folder, setFolder] = useState("");
  const [runtime, setRuntime] = useState<MCPRuntime>("docker");
  const [client, setClient] = useState("codex");
  const connection = mcpConnection({
    folder,
    runtime,
    readOnly,
    apiURL: apiUrl.replace(/\/api\/v1$/, ""),
    webURL: window.location.origin,
  });

  return (
    <details className="group min-w-0 rounded-xl border bg-card">
      <summary className="cursor-pointer px-4 py-4 text-sm font-medium">
        Advanced: use a local server
      </summary>
      <div className="min-w-0 space-y-5 border-t p-4">
        <p className="text-sm leading-relaxed text-muted-foreground">
          An alternative for clients that only support local commands. You need
          the Circular folder and Docker or Go on the computer running your
          coding assistant.
        </p>
        <div className="grid min-w-0 gap-4 sm:grid-cols-2">
          <ResourceSelect
            id="mcp-runtime"
            label="Run with"
            placeholder="Choose runtime"
            value={runtime}
            onValueChange={(value) => {
              if (value === "docker" || value === "binary") setRuntime(value);
            }}
            options={[
              { value: "docker", label: "Docker Compose" },
              { value: "binary", label: "Local Go binary" },
            ]}
          />
          <ResourceSelect
            id="mcp-local-client"
            label="Local client configuration"
            placeholder="Choose client"
            value={client}
            onValueChange={(value) => {
              if (value) setClient(value);
            }}
            options={[
              { value: "codex", label: "Codex · terminal command" },
              { value: "json", label: "Other clients · JSON configuration" },
            ]}
          />
          <div className="space-y-2 sm:col-span-2">
            <Label htmlFor="mcp-folder">Circular folder</Label>
            <Input
              id="mcp-folder"
              value={folder}
              onChange={(event) => setFolder(event.target.value)}
              placeholder="/absolute/path/to/circular"
              autoComplete="off"
              spellCheck={false}
              aria-describedby="mcp-folder-help"
              aria-invalid={!!folder && !connection}
            />
            <p id="mcp-folder-help" className="text-xs text-muted-foreground">
              The full installation path on your computer. These commands use a
              Linux or macOS terminal.
            </p>
            {!!folder && !connection && (
              <p role="alert" className="text-xs text-destructive">
                Enter an absolute folder path starting with / on a single line.
              </p>
            )}
          </div>
        </div>
        {connection && (
          <>
            <div className="space-y-3">
              <h3 className="text-sm font-semibold">Build once</h3>
              <p className="text-xs leading-relaxed text-muted-foreground">
                {runtime === "docker"
                  ? "Keep your Circular Compose stack running. Your client will launch this server when needed."
                  : "Requires Go 1.27.1 or later. Keep Circular’s API and worker running."}
              </p>
              <Snippet title="Build command" code={connection.install} />
            </div>
            <Snippet
              title={
                client === "codex"
                  ? "Local connection command"
                  : "MCP configuration"
              }
              code={client === "codex" ? connection.codex : connection.json}
            />
            <p className="text-xs leading-relaxed text-muted-foreground">
              {client === "codex"
                ? "Run the command where you use Codex, then start a new session."
                : "Merge this entry into your client’s MCP settings and reconnect. Check your client’s instructions; configuration formats can differ."}
            </p>
            <details className="space-y-3 text-sm">
              <summary className="cursor-pointer font-medium">
                Check the local server
              </summary>
              <p className="text-xs text-muted-foreground">
                Check API connectivity and version compatibility.
              </p>
              <Snippet title="Check command" code={connection.check} />
            </details>
          </>
        )}
      </div>
    </details>
  );
}

export function MCPSetup() {
  const [client, setClient] = useState("codex");
  const [access, setAccess] = useState("control");
  const readOnly = access === "read-only";
  const connection = httpMCPConnection(apiUrl, readOnly);
  const status = useQuery({
    queryKey: ["mcp-connection"],
    queryFn: api.mcpConnection,
    retry: false,
    refetchInterval: 10_000,
  });
  const available = status.isSuccess && status.data.available;
  const activity = status.data?.last_activity;

  return (
    <div className="grid min-w-0 items-start gap-6 xl:grid-cols-[minmax(0,1.4fr)_minmax(280px,0.8fr)]">
      <div className="min-w-0 space-y-5">
        <Card className="min-w-0">
          <CardHeader className="border-b">
            <div className="flex flex-wrap items-center gap-3">
              <CardTitle>
                <h2 className="flex items-center gap-2">
                  <Plug className="size-5" aria-hidden="true" />
                  Connect your coding agent
                </h2>
              </CardTitle>
              <Badge variant="secondary">Built in</Badge>
            </div>
            <CardDescription>
              Add Circular to your coding assistant to manage work from your
              conversation. The connection is included with Circular.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-6">
            <div className="grid min-w-0 gap-4 sm:grid-cols-2">
              <ResourceSelect
                id="mcp-client"
                label="Coding assistant"
                placeholder="Choose your assistant"
                value={client}
                onValueChange={(value) => {
                  if (value) setClient(value);
                }}
                options={[
                  { value: "codex", label: "Codex" },
                  { value: "url", label: "Other assistant" },
                ]}
              />
              <ResourceSelect
                id="mcp-access"
                label="Access"
                placeholder="Choose access"
                value={access}
                onValueChange={(value) => {
                  if (value) setAccess(value);
                }}
                options={[
                  { value: "control", label: "Full control" },
                  { value: "read-only", label: "Read only" },
                ]}
              />
            </div>
            <div className="space-y-3">
              <h3 className="text-sm font-semibold">
                {client === "codex"
                  ? "Run this once in your terminal"
                  : "Add this URL in your assistant’s MCP settings"}
              </h3>
              <Snippet
                title={
                  client === "codex" ? "Connection command" : "Connection URL"
                }
                code={client === "codex" ? connection.codex : connection.url}
              />
              <p className="text-sm leading-relaxed text-muted-foreground">
                {client === "codex"
                  ? "Then start a new Codex session. Keep Circular running while you use its tools."
                  : "Name the connection Circular and choose Streamable HTTP if asked. Reconnect your assistant, and keep Circular running."}
              </p>
              <p className="text-xs leading-relaxed text-muted-foreground">
                Use an assistant running on the same computer as Circular. No
                extra account or model API key is needed.
              </p>
            </div>
            <div className="space-y-3 border-t pt-5">
              <h3 className="text-sm font-semibold">
                Try it in your assistant
              </h3>
              <Snippet
                title="First prompt"
                code="Use Circular to list my projects and summarize the latest run."
              />
              <p className="text-xs leading-relaxed text-muted-foreground">
                Tasks and runs created by your assistant appear in this console.
                Starting a run uses the selected Circular agent and its existing
                model connection.
              </p>
            </div>
            <Link
              className="inline-block text-sm text-primary underline underline-offset-4"
              to="/docs/$"
              params={{ _splat: "coding-agent" }}
            >
              Read the connection guide
            </Link>
          </CardContent>
        </Card>
        <LocalConnection readOnly={readOnly} />
      </div>
      <div className="min-w-0 space-y-5">
        <Card>
          <CardHeader>
            <CardTitle>
              <h2>Connection status</h2>
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-lg border p-3">
              <span role="status" className="flex items-center gap-2">
                {status.isPending ? (
                  <LoaderCircle
                    className="size-4 animate-spin"
                    aria-hidden="true"
                  />
                ) : available ? (
                  <CheckCircle2
                    className="size-4 text-success"
                    aria-hidden="true"
                  />
                ) : null}
                {status.isPending
                  ? "Checking connection…"
                  : available
                    ? "Ready to add"
                    : "MCP unavailable"}
              </span>
              <Button
                size="sm"
                variant="ghost"
                disabled={status.isFetching}
                onClick={() => void status.refetch()}
              >
                {available ? "Refresh" : "Try again"}
              </Button>
            </div>
            {!status.isPending && !available && (
              <p
                role="alert"
                className="text-xs leading-relaxed text-destructive"
              >
                {status.error?.message ??
                  "Circular’s MCP connection is unavailable."}{" "}
                Make sure Circular is running, then try again.
              </p>
            )}
            {activity ? (
              <div className="space-y-1 text-xs leading-relaxed text-muted-foreground">
                <p className="font-medium text-foreground">
                  Last used by {activity.client_name || "a coding assistant"}
                </p>
                <p>
                  <time dateTime={activity.at}>
                    {new Date(activity.at).toLocaleString()}
                  </time>
                  {" · "}
                  {activity.access === "read-only"
                    ? "Read only"
                    : "Full control"}
                </p>
                <p>Your assistant reports whether its connection is active.</p>
              </div>
            ) : (
              <p className="text-xs leading-relaxed text-muted-foreground">
                No assistant activity yet. After adding Circular, try the prompt
                to confirm your assistant can use its tools.
              </p>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>
              <h2 className="flex items-center gap-2">
                <Workflow className="size-4" aria-hidden="true" />
                {readOnly ? "Read-only access" : "Full-control access"}
              </h2>
            </CardTitle>
            <CardDescription>
              Applies to every project in this Circular installation.
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4 text-sm">
            <ul className="space-y-3 text-muted-foreground">
              <li>Browse projects, repositories, agents and tasks.</li>
              <li>
                Read run progress, reports, changes and agent recommendations.
              </li>
              {!readOnly && (
                <>
                  <li>
                    Create projects, agents and tasks; choose models and
                    reasoning levels.
                  </li>
                  <li>
                    Accept agent recommendations, start runs and cancel work.
                  </li>
                </>
              )}
            </ul>
            <p className="border-t pt-4 text-xs leading-relaxed text-muted-foreground">
              To change an assistant’s access, select the new access level and
              update its connection using the new command or URL.
            </p>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
