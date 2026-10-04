import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  ArrowRight,
  Check,
  ExternalLink,
  FileText,
  LoaderCircle,
  Plus,
  RefreshCw,
} from "lucide-react";
import { api, ApiError } from "@/api";
import { ErrorAlert } from "@/components/error-alert";
import { ResourceSelect } from "@/components/resource-select";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type Installation = Awaited<
  ReturnType<typeof api.githubInstallations>
>["items"][number];
type CreateRequest = Parameters<typeof api.createGitHubRepository>[1];
type CreateResult = Awaited<ReturnType<typeof api.createGitHubRepository>>;
type PendingCreation = {
  request: CreateRequest;
  account: string;
  managementURL: string;
  result?: CreateResult;
};

const storageKey = (project: string) => `circular.github-create.${project}`;

function readPending(project: string): PendingCreation | null {
  try {
    const saved: unknown = JSON.parse(
      sessionStorage.getItem(storageKey(project)) ?? "null",
    );
    if (!saved || typeof saved !== "object" || !("request" in saved))
      return null;
    const value = saved as PendingCreation;
    if (
      typeof value.account !== "string" ||
      typeof value.managementURL !== "string" ||
      !value.request ||
      typeof value.request.installation_id !== "string" ||
      typeof value.request.name !== "string" ||
      typeof value.request.description !== "string" ||
      !/^[0-9a-f-]{36}$/i.test(value.request.request_key) ||
      !["private", "public"].includes(value.request.visibility)
    )
      return null;
    return value;
  } catch {
    return null;
  }
}

function savePending(project: string, value: PendingCreation | null) {
  try {
    if (value)
      sessionStorage.setItem(storageKey(project), JSON.stringify(value));
    else sessionStorage.removeItem(storageKey(project));
  } catch {
    // The current tab still retains the request when browser storage is unavailable.
  }
}

export function GitHubCreateRepository({
  project,
  installation,
  appURL,
}: {
  project: string;
  installation?: Installation;
  appURL: string;
}) {
  const client = useQueryClient();
  const [pending, setPending] = useState(() => readPending(project));
  const [open, setOpen] = useState(false);
  const [name, setName] = useState(pending?.request.name ?? "");
  const [description, setDescription] = useState(
    pending?.request.description ?? "",
  );
  const [visibility, setVisibility] = useState<"private" | "public">(
    pending?.request.visibility ?? "private",
  );
  const [completed, setCompleted] = useState<CreateResult | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const refresh = async () => {
    setRefreshing(true);
    try {
      await client.invalidateQueries({
        queryKey: ["integration", project, "github"],
      });
    } finally {
      setRefreshing(false);
    }
  };
  const create = useMutation({
    mutationKey: ["setup", "integrations"],
    mutationFn: (creation: PendingCreation) =>
      api.createGitHubRepository(project, creation.request),
    onSuccess: (result, creation) => {
      if (result.status === "attached") {
        setCompleted(result);
        setPending(null);
        savePending(project, null);
        void client.invalidateQueries({ queryKey: ["repositories", project] });
        void client.invalidateQueries({
          queryKey: ["integration", project, "github"],
        });
      } else {
        const next = { ...creation, result };
        setPending(next);
        savePending(project, next);
      }
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 422) {
        setPending(null);
        savePending(project, null);
      }
      if (error instanceof ApiError && error.status === 409)
        void client.invalidateQueries({ queryKey: ["connections", project] });
    },
  });
  const activeResult = completed ?? pending?.result;
  const needsAccess = activeResult?.status === "needs_access";
  const ready = completed?.status === "attached";
  const owner = pending?.account ?? installation?.account ?? "";
  const managementURL =
    activeResult?.management_url ||
    pending?.managementURL ||
    installation?.management_url;
  const permitted = installation?.can_create === true;
  const locked = !!pending || create.isPending;
  const showForm = !ready && (!!pending || permitted);

  return (
    <>
      <Button
        type="button"
        variant="outline"
        disabled={!installation && !pending}
        onClick={() => {
          if (!pending) {
            setCompleted(null);
            create.reset();
            setName("");
            setDescription("");
            setVisibility("private");
          }
          setOpen(true);
        }}
      >
        {pending ? (
          <RefreshCw aria-hidden="true" />
        ) : (
          <Plus aria-hidden="true" />
        )}
        {pending ? "Finish adding repository" : "Create repository"}
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="max-w-xl">
          <div className="space-y-2 pr-8">
            <DialogTitle className="text-lg font-semibold">
              {ready
                ? "Repository ready"
                : needsAccess
                  ? "Repository created — finish adding it"
                  : "Create a GitHub repository"}
            </DialogTitle>
            <DialogDescription className="text-sm leading-relaxed text-muted-foreground">
              {ready
                ? "Your new GitHub repository has been added to this Circular project."
                : "Create a repository on GitHub and add it to the selected Circular project."}
            </DialogDescription>
          </div>
          {!ready && !pending && !permitted && (
            <div className="mt-6 space-y-4">
              <div className="space-y-3 rounded-lg border bg-muted/20 p-4">
                <h3 className="text-sm font-medium">
                  Allow repository creation
                </h3>
                <p className="text-sm leading-relaxed text-muted-foreground">
                  {installation?.permission_message ||
                    "This GitHub App needs permission to create repositories for this account."}
                </p>
                <ol className="list-decimal space-y-2 pl-4 text-sm leading-relaxed text-muted-foreground">
                  <li>
                    The app owner opens its GitHub settings and sets Repository
                    permissions → Administration to Read and write.
                  </li>
                  <li>
                    The account or organization owner approves the updated
                    permissions under Manage GitHub access.
                  </li>
                  <li>Return here and refresh permissions.</li>
                </ol>
                {installation?.account_type === "User" && (
                  <p className="text-xs leading-relaxed text-muted-foreground">
                    For a personal repository, connect the GitHub account that
                    will own it.
                  </p>
                )}
              </div>
              <div className="flex flex-wrap gap-3">
                {appURL && (
                  <Button asChild variant="outline" size="sm">
                    <a href={appURL} target="_blank" rel="noreferrer">
                      Open GitHub App <ExternalLink aria-hidden="true" />
                    </a>
                  </Button>
                )}
                {managementURL && (
                  <Button asChild variant="outline" size="sm">
                    <a href={managementURL} target="_blank" rel="noreferrer">
                      Manage GitHub access <ExternalLink aria-hidden="true" />
                    </a>
                  </Button>
                )}
                <Button
                  type="button"
                  size="sm"
                  disabled={refreshing}
                  onClick={() => void refresh()}
                >
                  <RefreshCw
                    className={refreshing ? "animate-spin" : ""}
                    aria-hidden="true"
                  />
                  Refresh permissions
                </Button>
              </div>
            </div>
          )}
          {showForm && (
            <form
              aria-label="Create GitHub repository"
              className="mt-6 space-y-5"
              onSubmit={(event) => {
                event.preventDefault();
                if (create.isPending) return;
                const creation = pending ?? {
                  request: {
                    installation_id: installation!.id,
                    name: name.trim(),
                    description: description.trim(),
                    visibility,
                    request_key: crypto.randomUUID(),
                  },
                  account: installation!.account,
                  managementURL: installation!.management_url,
                };
                setPending(creation);
                savePending(project, creation);
                create.mutate(creation);
              }}
            >
              <div className="space-y-2">
                <p className="text-xs font-medium text-muted-foreground">
                  GitHub owner
                </p>
                <p className="break-words text-sm font-medium">{owner}</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="github-repository-name">Repository name</Label>
                <Input
                  id="github-repository-name"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  placeholder="my-new-project"
                  required
                  maxLength={100}
                  pattern={"[A-Za-z0-9_.\\-]+"}
                  disabled={locked}
                  autoComplete="off"
                  aria-describedby="github-repository-name-help"
                />
                <p
                  id="github-repository-name-help"
                  className="text-xs text-muted-foreground"
                >
                  Use letters, numbers, periods, hyphens, or underscores.
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="github-repository-description">
                  Description (optional)
                </Label>
                <Input
                  id="github-repository-description"
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  maxLength={350}
                  disabled={locked}
                  placeholder="What are you building?"
                />
              </div>
              <ResourceSelect
                id="github-repository-visibility"
                label="Visibility"
                value={visibility}
                onValueChange={(value) =>
                  setVisibility(value as "private" | "public")
                }
                options={[
                  { value: "private", label: "Private" },
                  { value: "public", label: "Public" },
                ]}
                placeholder="Private"
                disabled={locked}
              />
              <p className="text-xs leading-relaxed text-muted-foreground">
                {visibility === "private"
                  ? "Only people you grant access to can see this repository."
                  : "Anyone on the internet will be able to see this repository and its contents."}
              </p>
              <div className="flex gap-3 rounded-lg border bg-muted/20 p-3 text-sm">
                <FileText
                  className="mt-0.5 size-4 shrink-0 text-muted-foreground"
                  aria-hidden="true"
                />
                <p className="leading-relaxed">
                  <span className="font-medium">README included.</span> Your
                  repository starts with an initial commit, ready for an agent
                  to work on.
                </p>
              </div>
              {pending && !create.isPending && (
                <div
                  className="space-y-3 rounded-lg border bg-muted/20 p-4"
                  role="status"
                >
                  <p className="text-sm font-medium">
                    {needsAccess
                      ? "Allow access to your new repository"
                      : "Check repository creation"}
                  </p>
                  <p className="text-sm leading-relaxed text-muted-foreground">
                    {activeResult?.message ||
                      "The request is saved. Check its result before starting another repository. You can close this dialog and return to finish adding it."}
                  </p>
                  {needsAccess && managementURL && (
                    <Button asChild variant="outline" size="sm">
                      <a href={managementURL} target="_blank" rel="noreferrer">
                        Manage GitHub access <ExternalLink aria-hidden="true" />
                      </a>
                    </Button>
                  )}
                  {activeResult?.url && (
                    <a
                      href={activeResult.url}
                      target="_blank"
                      rel="noreferrer"
                      className="inline-flex items-center gap-1 text-sm underline underline-offset-4"
                    >
                      Open repository on GitHub{" "}
                      <ExternalLink className="size-3" aria-hidden="true" />
                    </a>
                  )}
                  <details className="text-xs leading-relaxed text-muted-foreground">
                    <summary className="cursor-pointer font-medium text-foreground">
                      Resolve this request manually
                    </summary>
                    <div className="mt-3 space-y-3">
                      <p>
                        Check {owner} on GitHub. If the repository exists, allow
                        the app to access it and use Add repository in Circular.
                        Once you have checked the result, you can clear this
                        saved request. Clearing it keeps any GitHub repository
                        and lets you start a new request.
                      </p>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => {
                          setPending(null);
                          savePending(project, null);
                          create.reset();
                          setOpen(false);
                        }}
                      >
                        Clear saved request
                      </Button>
                    </div>
                  </details>
                </div>
              )}
              {create.error && <ErrorAlert>{create.error.message}</ErrorAlert>}
              <div className="flex flex-wrap justify-end gap-3 border-t pt-5">
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => setOpen(false)}
                >
                  {pending ? "Close" : "Cancel"}
                </Button>
                <Button
                  type="submit"
                  disabled={create.isPending || (!pending && !name.trim())}
                >
                  {create.isPending && (
                    <LoaderCircle className="animate-spin" aria-hidden="true" />
                  )}
                  {create.isPending
                    ? "Adding repository…"
                    : pending
                      ? needsAccess
                        ? "Check access and add repository"
                        : "Check and finish adding"
                      : "Create and add repository"}
                </Button>
              </div>
            </form>
          )}
          {ready && completed && (
            <div className="mt-6 space-y-5">
              <div className="flex items-start gap-3 rounded-lg border bg-muted/20 p-4">
                <Check
                  className="mt-0.5 size-5 shrink-0 text-success"
                  aria-hidden="true"
                />
                <div className="min-w-0 space-y-2">
                  <p className="break-words text-sm font-medium">
                    {completed.name}
                  </p>
                  <a
                    href={completed.url}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 text-sm underline underline-offset-4"
                  >
                    Open on GitHub{" "}
                    <ExternalLink className="size-3" aria-hidden="true" />
                  </a>
                </div>
              </div>
              <p className="text-sm leading-relaxed text-muted-foreground">
                It is ready to select in the task launcher. Choose an agent and
                describe what you want to build.
              </p>
              <div className="flex flex-wrap justify-end gap-3">
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => setOpen(false)}
                >
                  Done
                </Button>
                <Button asChild>
                  <Link
                    to="/setup"
                    search={{ section: "repositories" }}
                    onClick={() => setOpen(false)}
                  >
                    View repositories <ArrowRight aria-hidden="true" />
                  </Link>
                </Button>
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
