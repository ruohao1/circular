import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { api, type CompletionPolicy, type TaskCompletionView } from "@/api";
import {
  completionAge,
  completionLabel,
  pullRequestNumber,
} from "@/lib/task-completion";
import { ErrorAlert } from "@/components/error-alert";
import { ResourceSelect } from "@/components/resource-select";
import { TaskPRReviews } from "@/components/run-pr-reviews";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";

const policyNames = {
  review_required: "Review required",
  direct_completion: "Direct completion",
};

export function TaskCompletion({
  taskID,
  disabled = false,
}: {
  taskID: string;
  disabled?: boolean;
}) {
  const client = useQueryClient();
  const queryKey = ["task-completion", taskID];
  const [editing, setEditing] = useState(false);
  const [mode, setMode] = useState<CompletionPolicy["mode"] | "">("");
  const [requirements, setRequirements] = useState<
    NonNullable<CompletionPolicy["requirements"]>
  >([]);
  const [number, setNumber] = useState("");
  const query = useQuery({
    queryKey,
    queryFn: () => api.taskCompletion(taskID),
    retry: false,
    refetchInterval: editing ? false : 10_000,
    refetchOnWindowFocus: !editing,
  });
  const saved = (value: TaskCompletionView) => {
    client.setQueryData(queryKey, value);
    for (const key of [
      ["task", taskID],
      ["task-planning"],
      ["task-planning-detail", taskID],
      ["task-timeline"],
    ])
      void client.invalidateQueries({ queryKey: key });
  };
  const policy = useMutation({
    mutationFn: () => {
      if (!mode || !query.data) throw new Error("Choose a completion policy.");
      return api.setTaskCompletionPolicy(taskID, {
        expected_revision: query.data.revision,
        policy: { mode, requirements },
      });
    },
    onSuccess: (value) => {
      saved(value);
      setEditing(false);
    },
    onError: () => void query.refetch(),
  });
  const refresh = useMutation({
    mutationFn: () => api.refreshTaskCompletion(taskID),
    onSuccess: saved,
  });
  const binding = useMutation({
    mutationFn: () => {
      const parsed = pullRequestNumber(number);
      if (!parsed || !query.data)
        throw new Error("Enter a positive PR number, such as 42.");
      return api.bindTaskPullRequest(taskID, {
        number: parsed,
        session_id: query.data.current_session_id,
        expected_revision: query.data.revision,
      });
    },
    onSuccess: saved,
    onError: () => void query.refetch(),
  });
  const value = query.data;
  const pending =
    disabled || policy.isPending || refresh.isPending || binding.isPending;
  const edit = () => {
    setMode(value?.policy?.mode ?? "");
    setRequirements(value?.policy?.requirements ?? []);
    policy.reset();
    setEditing(true);
  };
  const changeRequirement = (
    index: number,
    patch: Partial<(typeof requirements)[number]>,
  ) =>
    setRequirements((items) =>
      items.map((item, i) => (i === index ? { ...item, ...patch } : item)),
    );
  return (
    <section
      role="region"
      aria-label="Task completion"
      className="my-6 min-w-0 space-y-4 rounded-lg border p-4 [overflow-wrap:anywhere]"
    >
      <h2 className="font-semibold">Task completion</h2>
      {query.isPending && <p role="status">Loading completion requirements…</p>}
      {query.error && (
        <ErrorAlert>
          {query.error.message}
          <Button variant="link" onClick={() => void query.refetch()}>
            Retry completion
          </Button>
        </ErrorAlert>
      )}
      {value && (
        <>
          {!value.policy && (
            <p className="text-sm text-muted-foreground">
              Choose a completion policy before starting work.
            </p>
          )}
          {value.policy && (
            <div className="space-y-2">
              <Badge variant="outline">{policyNames[value.policy.mode]}</Badge>
              <p className="text-xs text-muted-foreground">
                {value.contract
                  ? "Frozen when work started"
                  : "Editable until work starts"}
              </p>
              <ul className="space-y-1 text-sm">
                {(value.policy.requirements ?? []).map((item) => (
                  <li key={item.key}>
                    <code>{item.key}</code> · {item.description}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {!value.contract && !editing && (
            <Button
              type="button"
              variant="outline"
              disabled={pending || value.owner_active}
              onClick={edit}
            >
              {value.policy
                ? "Edit completion policy"
                : "Set completion policy"}
            </Button>
          )}
          {editing && !value.contract && (
            <form
              className="space-y-3"
              onSubmit={(event) => {
                event.preventDefault();
                policy.mutate();
              }}
            >
              <fieldset disabled={pending} className="space-y-3">
                <ResourceSelect
                  id={`completion-policy-${taskID}`}
                  label="Completion policy"
                  value={mode}
                  onValueChange={(value) =>
                    setMode(value as CompletionPolicy["mode"])
                  }
                  options={Object.entries(policyNames).map(
                    ([value, label]) => ({ value, label }),
                  )}
                  placeholder="Choose policy"
                />
                <p className="text-xs text-muted-foreground">
                  Review required needs a PR, selected CI checks and an
                  independent review. Direct completion needs the required
                  result and evidence.
                </p>
                {requirements.map((item, index) => (
                  <div key={index} className="space-y-2 rounded border p-3">
                    <Label htmlFor={`${taskID}-requirement-${index}`}>
                      Requirement {index + 1} key
                    </Label>
                    <Input
                      id={`${taskID}-requirement-${index}`}
                      value={item.key}
                      required
                      pattern="[a-z][a-z0-9_]{0,63}"
                      maxLength={64}
                      onChange={(event) =>
                        changeRequirement(index, { key: event.target.value })
                      }
                    />
                    <Label htmlFor={`${taskID}-description-${index}`}>
                      Requirement {index + 1} description
                    </Label>
                    <Textarea
                      id={`${taskID}-description-${index}`}
                      required
                      maxLength={1000}
                      value={item.description}
                      onChange={(event) =>
                        changeRequirement(index, {
                          description: event.target.value,
                        })
                      }
                    />
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        setRequirements((items) =>
                          items.filter((_, i) => i !== index),
                        )
                      }
                    >
                      Remove requirement {index + 1}
                    </Button>
                  </div>
                ))}
                <Button
                  type="button"
                  variant="outline"
                  disabled={requirements.length >= 20}
                  onClick={() =>
                    setRequirements((items) => [
                      ...items,
                      { key: "", description: "" },
                    ])
                  }
                >
                  Add requirement
                </Button>
                <div className="flex flex-wrap gap-2">
                  <Button type="submit" disabled={!mode}>
                    Save completion policy
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    onClick={() => setEditing(false)}
                  >
                    Cancel policy edit
                  </Button>
                </div>
              </fieldset>
            </form>
          )}
          {value.contract && (
            <>
              <p role="status" className="font-medium">
                {completionLabel(value.verdict)}
              </p>
              <p className="text-xs text-muted-foreground">
                Completion evidence does not change the planning stage to Ready.
                Review-required work stays In Review until a verified merge and
                an explicit move to Done.
              </p>
              {value.verdict.blockers.length > 0 && (
                <ul
                  aria-label="Completion blockers"
                  className="space-y-3 text-sm"
                >
                  {value.verdict.blockers.map((blocker, i) => (
                    <li key={`${blocker.code}-${i}`}>
                      <strong>{blocker.message}</strong>
                      <p className="text-muted-foreground">{blocker.action}</p>
                    </li>
                  ))}
                </ul>
              )}
              {value.verdict.advisories.map((text, i) => (
                <p key={i} className="text-sm">
                  Advisory: {text}
                </p>
              ))}
            </>
          )}
          {value.report && (
            <div className="space-y-2 border-t pt-3">
              <h3 className="text-sm font-semibold">Accepted coding result</h3>
              <p className="whitespace-pre-line text-sm">
                {value.report.result}
              </p>
              <ul className="space-y-3 text-sm">
                {(value.report.evidence ?? []).map((item) => (
                  <li key={item.key}>
                    <strong>{item.key}</strong>
                    <p className="whitespace-pre-line">{item.detail}</p>
                    {item.links?.map((link) => (
                      <a
                        key={link}
                        href={link}
                        target="_blank"
                        rel="noreferrer"
                        className="mr-3 underline"
                      >
                        {link}
                      </a>
                    ))}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {value.policy?.mode === "review_required" && (
            <>
              <p className="text-xs text-muted-foreground">
                Publish this Task's PR manually. Required checks and reviewer
                setup are in{" "}
                <Link
                  to="/setup"
                  search={{ section: "repositories" }}
                  className="underline"
                >
                  Repository settings
                </Link>{" "}
                and{" "}
                <Link
                  to="/setup"
                  search={{ section: "integrations" }}
                  className="underline"
                >
                  Integrations
                </Link>
                .
              </p>
              {value.binding && (
                <p className="text-sm">
                  Bound pull request:{" "}
                  {value.observation?.pr.url ? (
                    <a
                      href={value.observation.pr.url}
                      target="_blank"
                      rel="noreferrer"
                      className="underline"
                    >
                      #{value.binding.number}
                    </a>
                  ) : (
                    `#${value.binding.number}`
                  )}
                </p>
              )}
              {value.report && (
                <form
                  onSubmit={(event) => {
                    event.preventDefault();
                    binding.mutate();
                  }}
                  className="flex flex-wrap items-end gap-2"
                >
                  <div className="min-w-0 flex-1 space-y-1">
                    <Label htmlFor={`completion-pr-${taskID}`}>
                      Pull request number
                    </Label>
                    <Input
                      id={`completion-pr-${taskID}`}
                      inputMode="numeric"
                      value={number}
                      onChange={(event) => setNumber(event.target.value)}
                      placeholder={String(value.binding?.number ?? "42")}
                      disabled={pending}
                    />
                  </div>
                  <Button type="submit" variant="outline" disabled={pending}>
                    Bind pull request
                  </Button>
                </form>
              )}
              <div className="flex flex-wrap items-center gap-3">
                <Button
                  type="button"
                  variant="outline"
                  disabled={pending || !value.binding}
                  onClick={() => refresh.mutate()}
                >
                  Refresh completion evidence
                </Button>
                <span className="text-xs text-muted-foreground">
                  Observed:{" "}
                  {completionAge(value.observation?.checked_at ?? null)}
                </span>
              </div>
              {value.observation && (
                <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-xs [&>dd]:min-w-0 [&>dd]:font-mono">
                  <dt>Base</dt>
                  <dd>{value.observation.revision.base_sha || "Unverified"}</dd>
                  <dt>Head</dt>
                  <dd>{value.observation.revision.head_sha || "Unverified"}</dd>
                  <dt>Tested</dt>
                  <dd>
                    {value.observation.revision.tested_sha || "Unverified"}
                  </dd>
                </dl>
              )}
              <ul aria-label="Required CI checks" className="space-y-2 text-sm">
                {value.contract?.check_config.checks.map((check) => {
                  const evidence = value.observation?.checks?.find(
                    (item) =>
                      item.check.workflow_id === check.workflow_id &&
                      item.check.app_id === check.app_id &&
                      item.check.name === check.name,
                  );
                  return (
                    <li key={`${check.workflow_id}:${check.name}`}>
                      <span className="font-medium">{check.name}</span> ·{" "}
                      {evidence?.conclusion ||
                        evidence?.run_status ||
                        "No verified result"}
                      <p className="text-xs text-muted-foreground">
                        {check.workflow_path} · workflow {check.workflow_id} ·
                        app {check.app_id}
                      </p>
                      {evidence?.url && (
                        <a
                          className="underline"
                          href={evidence.url}
                          target="_blank"
                          rel="noreferrer"
                        >
                          View check attempt {evidence.attempt}
                        </a>
                      )}
                    </li>
                  );
                })}
              </ul>
              {value.review && (
                <div className="space-y-1 text-sm">
                  <h3 className="font-semibold">
                    Independent review · {value.review.state}
                  </h3>
                  <p>Coverage: {value.review.coverage || "Pending"}</p>
                  <ul>
                    {value.review.findings?.map((finding, i) => (
                      <li key={i}>
                        {finding.severity}: {finding.title}
                      </li>
                    ))}
                  </ul>
                  <Link
                    to="/runs/$runId"
                    params={{ runId: value.review.run_id }}
                    className="underline"
                  >
                    Read independent report
                  </Link>
                </div>
              )}
              {value.report && value.binding && (
                <TaskPRReviews taskID={taskID} />
              )}
            </>
          )}
        </>
      )}
      {(policy.error || binding.error || refresh.error) && (
        <ErrorAlert>
          {(policy.error || binding.error || refresh.error)?.message}
        </ErrorAlert>
      )}
    </section>
  );
}
