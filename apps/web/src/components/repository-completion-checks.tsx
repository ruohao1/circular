import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, type CompletionCheckSetup } from "@/api";
import { initialCompletionChecks } from "@/lib/task-completion";
import { ErrorAlert } from "./error-alert";
import { Button } from "./ui/button";

type Check = CompletionCheckSetup["available"][number];
const key = (check: Check) =>
  JSON.stringify([check.workflow_id, check.app_id, check.name]);

export function RepositoryCompletionChecks({
  repositoryID,
}: {
  repositoryID: string;
}) {
  const [open, setOpen] = useState(false);
  const [selection, setSelection] = useState<Check[]>();
  const client = useQueryClient();
  const queryKey = ["repository-completion-checks", repositoryID];
  const query = useQuery({
    queryKey,
    queryFn: () => api.repositoryCompletionChecks(repositoryID),
    enabled: open,
    retry: false,
  });
  const selected =
    selection ?? (query.data ? initialCompletionChecks(query.data) : []);
  const save = useMutation({
    mutationFn: () =>
      api.setRepositoryCompletionChecks(repositoryID, {
        expected_revision: query.data?.config?.revision ?? 0,
        checks: selected,
      }),
    onSuccess: (config) => {
      client.setQueryData<CompletionCheckSetup>(
        queryKey,
        (previous) => previous && { ...previous, config },
      );
      setSelection(undefined);
    },
    onError: () => void query.refetch(),
  });
  return (
    <details
      className="min-w-0 rounded-lg border p-3"
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary className="cursor-pointer text-sm font-medium">
        Required completion checks
      </summary>
      <div className="mt-3 space-y-3">
        <p className="text-xs text-muted-foreground">
          Select the GitHub Actions jobs required for new review-required Tasks.
          Save the selection explicitly. Tasks that already started keep their
          frozen checks.
        </p>
        {query.isFetching && (
          <p role="status" className="text-sm">
            Discovering workflow jobs…
          </p>
        )}
        {(query.error || query.data?.reason) && (
          <ErrorAlert>
            {query.error?.message || query.data?.reason}{" "}
            <Link
              to="/setup"
              search={{ section: "integrations" }}
              className="underline"
            >
              GitHub App settings
            </Link>
          </ErrorAlert>
        )}
        {query.data && !query.data.reason && (
          <>
            {!query.data.available.length && (
              <p className="text-sm">
                No supported jobs found. Run a GitHub Actions workflow, then
                refresh.
              </p>
            )}
            <fieldset disabled={save.isPending} className="space-y-2">
              <legend className="sr-only">Required workflow jobs</legend>
              {query.data.available.map((check) => (
                <label
                  key={key(check)}
                  className="flex cursor-pointer items-start gap-3 rounded border p-3 text-sm"
                >
                  <input
                    type="checkbox"
                    className="mt-1 size-4 shrink-0"
                    checked={selected.some((item) => key(item) === key(check))}
                    onChange={(event) =>
                      setSelection(
                        event.target.checked
                          ? [...selected, check]
                          : selected.filter((item) => key(item) !== key(check)),
                      )
                    }
                  />
                  <span className="min-w-0 [overflow-wrap:anywhere]">
                    {check.name}
                    <span className="block text-xs text-muted-foreground">
                      {check.workflow_path} · workflow {check.workflow_id} · app{" "}
                      {check.app_id}
                    </span>
                  </span>
                </label>
              ))}
            </fieldset>
            <Button
              type="button"
              variant="outline"
              disabled={save.isPending || query.isFetching || !selected.length}
              onClick={() => save.mutate()}
            >
              Save required checks
            </Button>
          </>
        )}
        <Button
          type="button"
          variant="ghost"
          disabled={query.isFetching || save.isPending}
          onClick={() => void query.refetch()}
        >
          Refresh available checks
        </Button>
        {save.error && <ErrorAlert>{save.error.message}</ErrorAlert>}
        {save.isSuccess && (
          <p role="status" className="text-sm">
            Required checks saved.
          </p>
        )}
      </div>
    </details>
  );
}
