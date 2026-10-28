import { useState } from "react";
import {
  Link,
  useNavigate,
  useParams,
  useSearch,
} from "@tanstack/react-router";
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { api, type TaskProposal, type TaskProposalDraft } from "@/api";
import { useProject, useProjectSelectionLock } from "@/use-project";
import { Markdown } from "@/components/markdown";
import { ErrorAlert } from "@/components/error-alert";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

const emptyDraft: TaskProposalDraft = {
  title: "",
  purpose: "",
  description: "",
  acceptance_criteria: [],
  open_questions: [],
  repository_id: null,
  parent_task_id: null,
  prerequisite_task_ids: [],
  sources: [],
  priority: "none",
  preferred_agent_id: null,
  planned_start_date: null,
  planned_finish_date: null,
  completion_policy: { mode: "unresolved", requirements: [] },
};
const lines = (value: string) =>
  value
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
function DraftForm({
  initial,
  project,
  pending,
  sourceLocked = false,
  onSave,
  onCancel,
}: {
  initial: TaskProposalDraft;
  project: string;
  pending: boolean;
  sourceLocked?: boolean;
  onSave: (draft: TaskProposalDraft) => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState(initial);
  const [criteria, setCriteria] = useState(
    initial.acceptance_criteria.join("\n"),
  );
  const [questions, setQuestions] = useState(initial.open_questions.join("\n"));
  const [dependencies, setDependencies] = useState(
    initial.prerequisite_task_ids.join(", "),
  );
  const repos = useQuery({
    queryKey: ["repositories", project],
    queryFn: () => api.repositories(project),
    enabled: !!project,
  });
  const change = (patch: Partial<TaskProposalDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        onSave({
          ...draft,
          acceptance_criteria: lines(criteria),
          open_questions: lines(questions),
          prerequisite_task_ids: dependencies
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean),
        });
      }}
      className="space-y-5"
    >
      {sourceLocked && (
        <p className="text-sm text-muted-foreground">
          Title, scope, and Repository come from the source record. Change them
          at the source and import again. Clarify purpose, acceptance criteria,
          and completion here.
        </p>
      )}
      <fieldset disabled={pending} className="space-y-5">
        <div className="space-y-2">
          <Label htmlFor="proposal-title">Title</Label>
          <Input
            id="proposal-title"
            disabled={sourceLocked}
            required
            maxLength={500}
            value={draft.title}
            onChange={(e) => change({ title: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="proposal-purpose">Purpose</Label>
          <Textarea
            id="proposal-purpose"
            value={draft.purpose}
            maxLength={4000}
            onChange={(e) => change({ purpose: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="proposal-scope">Scope</Label>
          <Textarea
            id="proposal-scope"
            disabled={sourceLocked}
            value={draft.description}
            maxLength={20000}
            rows={7}
            onChange={(e) => change({ description: e.target.value })}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="proposal-criteria">Acceptance criteria</Label>
          <p className="text-sm text-muted-foreground">
            Write one observable result per line.
          </p>
          <Textarea
            id="proposal-criteria"
            value={criteria}
            onChange={(e) => setCriteria(e.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="proposal-questions">Open questions</Label>
          <Textarea
            id="proposal-questions"
            value={questions}
            onChange={(e) => setQuestions(e.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="proposal-repository">Repository</Label>
          <select
            id="proposal-repository"
            disabled={sourceLocked}
            className="w-full rounded-md border bg-background p-2"
            value={draft.repository_id ?? ""}
            onChange={(e) => change({ repository_id: e.target.value || null })}
          >
            <option value="">Choose later</option>
            {repos.data?.map((repo) => (
              <option key={repo.id} value={repo.id}>
                {repo.name}
              </option>
            ))}
          </select>
        </div>
        <div className="space-y-2">
          <Label htmlFor="proposal-dependencies">Prerequisite Task IDs</Label>
          <Input
            id="proposal-dependencies"
            value={dependencies}
            onChange={(e) => setDependencies(e.target.value)}
          />
          <p className="text-sm text-muted-foreground">
            Separate IDs with commas. Work waits until these Tasks are
            completed.
          </p>
        </div>
        <div className="space-y-2">
          <Label htmlFor="proposal-completion">Completion policy</Label>
          <select
            id="proposal-completion"
            className="w-full rounded-md border bg-background p-2"
            value={draft.completion_policy.mode}
            onChange={(e) => {
              const mode = e.target.value;
              if (
                mode === "unresolved" ||
                mode === "direct_completion" ||
                mode === "review_required"
              )
                change({
                  completion_policy:
                    mode === "unresolved"
                      ? { mode, requirements: [] }
                      : {
                          mode,
                          requirements:
                            draft.completion_policy.requirements ?? [],
                        },
                });
            }}
          >
            <option value="unresolved">Needs a decision</option>
            <option value="review_required">Review required</option>
            <option value="direct_completion">Direct completion</option>
          </select>
        </div>
        {draft.completion_policy.mode !== "unresolved" && (
          <section className="space-y-3">
            <h3 className="font-medium">Required completion evidence</h3>
            {(draft.completion_policy.requirements ?? []).map(
              (requirement, index) => (
                <div key={index} className="space-y-2 rounded-md border p-3">
                  <Label htmlFor={`requirement-key-${index}`}>
                    Evidence key
                  </Label>
                  <Input
                    id={`requirement-key-${index}`}
                    value={requirement.key}
                    maxLength={80}
                    onChange={(e) =>
                      change({
                        completion_policy: {
                          ...draft.completion_policy,
                          requirements: (
                            draft.completion_policy.requirements ?? []
                          ).map((r, i) =>
                            i === index ? { ...r, key: e.target.value } : r,
                          ),
                        },
                      })
                    }
                  />
                  <Label htmlFor={`requirement-description-${index}`}>
                    Required evidence
                  </Label>
                  <Input
                    id={`requirement-description-${index}`}
                    value={requirement.description}
                    maxLength={1000}
                    onChange={(e) =>
                      change({
                        completion_policy: {
                          ...draft.completion_policy,
                          requirements: (
                            draft.completion_policy.requirements ?? []
                          ).map((r, i) =>
                            i === index
                              ? { ...r, description: e.target.value }
                              : r,
                          ),
                        },
                      })
                    }
                  />
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() =>
                      change({
                        completion_policy: {
                          ...draft.completion_policy,
                          requirements: (
                            draft.completion_policy.requirements ?? []
                          ).filter((_, i) => i !== index),
                        },
                      })
                    }
                  >
                    Remove evidence {index + 1}
                  </Button>
                </div>
              ),
            )}
            <Button
              type="button"
              variant="outline"
              disabled={
                (draft.completion_policy.requirements?.length ?? 0) >= 20
              }
              onClick={() =>
                change({
                  completion_policy: {
                    ...draft.completion_policy,
                    requirements: [
                      ...(draft.completion_policy.requirements ?? []),
                      { key: "", description: "" },
                    ],
                  },
                })
              }
            >
              Add required evidence
            </Button>
          </section>
        )}
      </fieldset>
      <div className="flex gap-3">
        <Button type="submit" disabled={pending}>
          Save proposal
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={pending}
          onClick={onCancel}
        >
          Cancel
        </Button>
      </div>
    </form>
  );
}
function SignIn({ returnPath }: { returnPath: string }) {
  const cache = useQueryClient();
  const session = useQuery({
    queryKey: ["approval-session"],
    queryFn: api.approvalSession,
    retry: false,
  });
  const start = useMutation({
    mutationFn: () => api.startApprovalLogin(returnPath),
    onSuccess: (value) => window.location.assign(value.url),
  });
  const logout = useMutation({
    mutationFn: () => api.endApprovalSession(session.data?.csrf_token ?? ""),
    onSuccess: () =>
      cache.invalidateQueries({ queryKey: ["approval-session"] }),
  });
  if (session.error) return <ErrorAlert>{session.error.message}</ErrorAlert>;
  return (
    <div className="space-y-3 rounded-lg border p-4">
      {session.data?.user ? (
        <div className="flex items-center justify-between gap-3">
          <p>
            Signed in as <strong>{session.data.user.login}</strong>
          </p>
          <Button
            variant="outline"
            onClick={() => logout.mutate()}
            disabled={logout.isPending}
          >
            Sign out
          </Button>
        </div>
      ) : (
        <>
          <p>A separate sign-in is required to approve or reject work.</p>
          {session.data && !session.data.configured ? (
            <p className="text-sm text-muted-foreground">
              An instance owner must configure GitHub sign-in and the approver
              allowlist. Proposals remain pending.
            </p>
          ) : (
            <Button
              disabled={!session.data || start.isPending}
              onClick={() => start.mutate()}
            >
              Sign in with GitHub
            </Button>
          )}
        </>
      )}
      {(start.error || logout.error) && (
        <ErrorAlert>{(start.error || logout.error)?.message}</ErrorAlert>
      )}
    </div>
  );
}
export function ProposalsPage() {
  const { selectedProject, project } = useProject();
  const navigate = useNavigate();
  const search = useSearch({ strict: false });
  const [draftProject, setDraftProject] = useState("");
  const [key, setKey] = useState(() => crypto.randomUUID());
  const list = useQuery({
    queryKey: ["task-proposals", selectedProject],
    queryFn: () => api.taskProposals(selectedProject),
    enabled: !!selectedProject,
  });
  const create = useMutation({
    mutationFn: (draft: TaskProposalDraft) =>
      api.proposeTask({
        project_id: draftProject,
        request_key: key,
        proposer: "Circular console",
        draft,
      }),
    onSuccess: (p) => {
      setKey(crypto.randomUUID());
      void navigate({
        to: "/proposals/$proposalId",
        params: { proposalId: p.id },
        search: { revision: p.revision },
      });
    },
  });
  useProjectSelectionLock(!!draftProject || create.isPending);
  return (
    <main className="mx-auto max-w-4xl space-y-6 p-6">
      <div className="flex items-center justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold">Task proposals</h1>
          <p className="text-muted-foreground">
            {project?.name ?? "Choose a Project"}
          </p>
        </div>
        <Button
          disabled={!selectedProject || !!draftProject}
          onClick={() => setDraftProject(selectedProject)}
        >
          New proposal
        </Button>
      </div>
      {search.login_error && (
        <ErrorAlert>
          Sign-in could not be verified. Use an account on this instance’s
          approver allowlist and try again.
        </ErrorAlert>
      )}
      <SignIn returnPath="/proposals" />
      {draftProject ? (
        <Card>
          <CardHeader>
            <CardTitle>Propose work</CardTitle>
          </CardHeader>
          <CardContent>
            <DraftForm
              key={draftProject}
              initial={emptyDraft}
              project={draftProject}
              pending={create.isPending}
              onSave={(draft) => create.mutate(draft)}
              onCancel={() => setDraftProject("")}
            />
            {create.error && <ErrorAlert>{create.error.message}</ErrorAlert>}
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-3">
          {list.data?.map((p) => (
            <Link
              key={p.id}
              to="/proposals/$proposalId"
              params={{ proposalId: p.id }}
              search={{ revision: p.revision }}
              className="flex items-center justify-between gap-4 rounded-lg border p-4 hover:bg-muted"
            >
              <div>
                <p className="font-medium">{p.draft.title}</p>
                <p className="text-sm text-muted-foreground">
                  Revision {p.revision} · {p.proposer || "Unspecified proposer"}
                </p>
              </div>
              <Badge variant="outline">{p.status}</Badge>
            </Link>
          ))}
          {list.data?.length === 0 && <p>No proposals in this Project.</p>}
          {list.error && <ErrorAlert>{list.error.message}</ErrorAlert>}
        </div>
      )}
    </main>
  );
}
function ProposalReview({ proposal: p }: { proposal: TaskProposal }) {
  const { projects } = useProject();
  const query = useQueryClient();
  const navigate = useNavigate();
  const search = useSearch({ strict: false });
  const [editing, setEditing] = useState(false);
  const [reason, setReason] = useState("");
  const session = useQuery({
    queryKey: ["approval-session"],
    queryFn: api.approvalSession,
    retry: false,
  });
  const target = useQuery({
    queryKey: ["task-planning-detail", p.target_task_id],
    queryFn: () => api.taskPlanningDetail(p.target_task_id!),
    enabled: !!p.target_task_id,
  });
  const taskChanged =
    !!target.data && target.data.task.revision !== p.expected_task_revision;
  const targetUnavailable = !!p.target_task_id && !target.data;
  const sourceLocked =
    !!p.source.linear || target.data?.content_lock_reason === "source_managed";
  const stale = search.revision !== p.revision;
  const revise = useMutation({
    mutationFn: (draft: TaskProposalDraft) =>
      api.reviseTaskProposal(p.id, {
        expected_revision: p.revision,
        expected_task_revision:
          target.data?.task.revision ?? p.expected_task_revision,
        draft,
      }),
    onSuccess: (next) => {
      query.setQueryData(["task-proposal", p.id], next);
      setEditing(false);
      void navigate({
        to: "/proposals/$proposalId",
        params: { proposalId: p.id },
        search: { revision: next.revision },
      });
    },
  });
  const decide = useMutation({
    mutationFn: (decision: "approved" | "rejected") =>
      api.decideTaskProposal(
        p.id,
        { expected_revision: p.revision, decision, reason },
        session.data?.csrf_token ?? "",
      ),
    onSuccess: (next) => {
      query.setQueryData(["task-proposal", p.id], next);
      void query.invalidateQueries({ queryKey: ["task-proposals"] });
      for (const key of [
        "task-planning",
        "task-planning-detail",
        "task-approval",
        "task-completion",
        "task-timeline",
        "task",
      ])
        void query.invalidateQueries({ queryKey: [key] });
    },
    onError: () => {
      void query.invalidateQueries({ queryKey: ["approval-session"] });
      void query.invalidateQueries({ queryKey: ["task-proposal", p.id] });
      void query.invalidateQueries({
        queryKey: ["task-planning-detail", p.target_task_id],
      });
    },
  });
  const clear =
    !!p.draft.purpose.trim() &&
    !!p.draft.description.trim() &&
    p.draft.acceptance_criteria.length > 0 &&
    p.draft.open_questions.length === 0 &&
    p.draft.completion_policy.mode !== "unresolved";
  const project = projects.data?.find((x) => x.id === p.project_id);
  const repos = useQuery({
    queryKey: ["repositories", p.project_id],
    queryFn: () => api.repositories(p.project_id),
  });
  const repository = repos.data?.find((r) => r.id === p.draft.repository_id);
  const agents = useQuery({
    queryKey: ["agents", p.project_id],
    queryFn: () => api.agents(p.project_id),
    enabled: !!p.draft.preferred_agent_id,
  });
  const preferredAgent = agents.data?.find(
    (a) => a.id === p.draft.preferred_agent_id,
  );
  return (
    <main className="mx-auto max-w-4xl space-y-6 p-6">
      <Link
        to="/proposals"
        search={{ login_error: undefined }}
        className="text-sm underline"
      >
        All proposals
      </Link>
      <header className="space-y-2">
        <div className="flex flex-wrap items-center gap-3">
          <Badge variant="outline">{p.status}</Badge>
          <span className="text-sm text-muted-foreground">
            {project?.name ?? "Project"} · Revision {p.revision}
          </span>
        </div>
        <h1 className="text-2xl font-semibold">{p.draft.title}</h1>
        <p className="text-sm text-muted-foreground">
          Proposed by {p.proposer || "an unspecified client"}.
        </p>
      </header>
      {stale && p.status !== "approved" && (
        <div role="status" className="rounded-lg border p-4">
          This link does not identify the current revision.{" "}
          <Link
            to="/proposals/$proposalId"
            params={{ proposalId: p.id }}
            search={{ revision: p.revision }}
            className="underline"
          >
            Review latest revision
          </Link>
        </div>
      )}
      {taskChanged && p.status !== "approved" && (
        <p role="status" className="rounded-lg border p-4">
          The existing Task changed after this proposal was prepared. Open the
          current Task below, then revise the proposal before approving it.
        </p>
      )}
      {target.error && <ErrorAlert>{target.error.message}</ErrorAlert>}
      {editing ? (
        <Card>
          <CardContent className="pt-6">
            <DraftForm
              key={p.revision}
              initial={p.draft}
              sourceLocked={sourceLocked}
              project={p.project_id}
              pending={revise.isPending}
              onSave={(draft) => revise.mutate(draft)}
              onCancel={() => setEditing(false)}
            />
          </CardContent>
        </Card>
      ) : (
        <Card>
          <CardContent className="space-y-6 pt-6">
            <section>
              <h2 className="mb-2 font-semibold">Purpose</h2>
              <p className="whitespace-pre-wrap">
                {p.draft.purpose || "Needs clarification"}
              </p>
            </section>
            <section>
              <h2 className="mb-2 font-semibold">Scope</h2>
              <Markdown>
                {p.draft.description || "Needs clarification"}
              </Markdown>
            </section>
            <section>
              <h2 className="mb-2 font-semibold">Acceptance criteria</h2>
              {p.draft.acceptance_criteria.length ? (
                <ul className="list-disc space-y-1 pl-5">
                  {p.draft.acceptance_criteria.map((text, i) => (
                    <li key={i}>{text}</li>
                  ))}
                </ul>
              ) : (
                <p>Needs clarification</p>
              )}
            </section>
            {p.draft.open_questions.length > 0 && (
              <section>
                <h2 className="mb-2 font-semibold">Open questions</h2>
                <ul className="list-disc pl-5">
                  {p.draft.open_questions.map((text, i) => (
                    <li key={i}>{text}</li>
                  ))}
                </ul>
              </section>
            )}
            <section>
              <h2 className="mb-2 font-semibold">Completion</h2>
              <p>
                {p.draft.completion_policy.mode === "review_required"
                  ? "Review required"
                  : p.draft.completion_policy.mode === "direct_completion"
                    ? "Direct completion"
                    : "Needs a decision"}
              </p>
              <ul className="list-disc pl-5">
                {p.draft.completion_policy.requirements?.map((r) => (
                  <li key={r.key}>{r.description}</li>
                ))}
              </ul>
            </section>
            <section>
              <h2 className="mb-2 font-semibold">Repository and planning</h2>
              <p>
                {repository?.name ??
                  (p.draft.repository_id
                    ? `Repository ${p.draft.repository_id}`
                    : "No Repository selected. Work will remain Open.")}
              </p>
              {p.draft.parent_task_id && (
                <p>
                  Parent:{" "}
                  <Link
                    to="/tasks"
                    search={{ taskId: p.draft.parent_task_id }}
                    className="underline"
                  >
                    {p.draft.parent_task_id}
                  </Link>
                </p>
              )}
              <p>Priority: {p.draft.priority}</p>
              {p.draft.preferred_agent_id && (
                <p>
                  Preferred Agent:{" "}
                  {preferredAgent?.name ?? p.draft.preferred_agent_id}
                </p>
              )}
              {(p.draft.planned_start_date || p.draft.planned_finish_date) && (
                <p>
                  Planned: {p.draft.planned_start_date ?? "No start date"} –{" "}
                  {p.draft.planned_finish_date ?? "No finish date"}
                </p>
              )}
            </section>
            <section>
              <h2 className="mb-2 font-semibold">Dependencies</h2>
              <ProposalDependencies ids={p.draft.prerequisite_task_ids} />
              <p className="mt-2 text-sm text-muted-foreground">
                Work becomes Ready only when its requirements and dependencies
                are satisfied.
              </p>
            </section>
            {p.draft.sources.length > 0 && (
              <section>
                <h2 className="mb-2 font-semibold">Sources</h2>
                <ul>
                  {p.draft.sources.map((source, i) => (
                    <li key={i}>
                      <a
                        href={source.url}
                        target="_blank"
                        rel="noreferrer"
                        className="underline"
                      >
                        {source.label || source.url}
                      </a>
                    </li>
                  ))}
                </ul>
                <p className="text-sm text-muted-foreground">
                  Source links provide context. They do not prove user approval.
                </p>
              </section>
            )}
            {p.target_task_id && (
              <p>
                This proposal preserves{" "}
                <Link
                  to="/tasks"
                  search={{ taskId: p.target_task_id }}
                  className="underline"
                >
                  the existing Task
                </Link>{" "}
                and its history.
              </p>
            )}
          </CardContent>
        </Card>
      )}
      {p.status !== "approved" && !editing && (
        <Button
          variant="outline"
          disabled={decide.isPending || stale || targetUnavailable}
          onClick={() => setEditing(true)}
        >
          Revise proposal
        </Button>
      )}
      {p.status === "pending" ? (
        <>
          <SignIn returnPath={`/proposals/${p.id}?revision=${p.revision}`} />
          {!clear && (
            <p role="status">
              Resolve the open questions and provide purpose, scope, acceptance
              criteria, and a completion policy before approval.
            </p>
          )}
          {session.data?.user && (
            <div className="space-y-3">
              <Label htmlFor="decision-reason">Decision note (optional)</Label>
              <Textarea
                id="decision-reason"
                value={reason}
                maxLength={4000}
                onChange={(e) => setReason(e.target.value)}
              />
              <div className="flex gap-3">
                <Button
                  disabled={
                    stale ||
                    taskChanged ||
                    targetUnavailable ||
                    editing ||
                    !clear ||
                    decide.isPending
                  }
                  onClick={() => decide.mutate("approved")}
                >
                  Approve revision {p.revision}
                </Button>
                <Button
                  variant="outline"
                  disabled={stale || editing || decide.isPending}
                  onClick={() => decide.mutate("rejected")}
                >
                  Reject revision {p.revision}
                </Button>
              </div>
            </div>
          )}
          <p className="text-sm text-muted-foreground">
            Approval records the agreed scope. Starting an agent, publishing,
            and merging remain separate actions.
          </p>
        </>
      ) : (
        p.decision && (
          <div className="space-y-2 rounded-lg border p-4">
            <p className="font-semibold">
              {p.status === "approved" ? "Approved" : "Rejected"} by{" "}
              {p.decision.approver_login}
            </p>
            <p className="text-sm text-muted-foreground">
              Revision {p.decision.revision} ·{" "}
              {new Date(p.decision.created_at).toLocaleString()}
            </p>
            {p.decision.reason && <p>{p.decision.reason}</p>}
            {p.task_id && (
              <Link
                to="/tasks"
                search={{ taskId: p.task_id }}
                className="underline"
              >
                Open Task
              </Link>
            )}
          </div>
        )
      )}
      {(revise.error || decide.error) && (
        <ErrorAlert>{(revise.error || decide.error)?.message}</ErrorAlert>
      )}
    </main>
  );
}
export function ProposalPage() {
  const params = useParams({ strict: false });
  const id = params.proposalId ?? "";
  const proposal = useQuery({
    queryKey: ["task-proposal", id],
    queryFn: () => api.taskProposal(id),
    enabled: !!id,
    retry: false,
  });
  if (proposal.error)
    return (
      <div className="p-6">
        <ErrorAlert>{proposal.error.message}</ErrorAlert>
      </div>
    );
  if (!proposal.data)
    return (
      <p className="p-6" role="status">
        Loading proposal…
      </p>
    );
  return <ProposalReview key={proposal.data.id} proposal={proposal.data} />;
}

function ProposalDependencies({ ids }: { ids: string[] }) {
  const tasks = useQueries({
    queries: ids.map((id) => ({
      queryKey: ["task", id],
      queryFn: () => api.task(id),
      refetchInterval: 10000,
    })),
  });
  if (!ids.length) return <p>No prerequisites.</p>;
  return (
    <ul className="space-y-1">
      {ids.map((id, index) => (
        <li key={id}>
          <Link to="/tasks" search={{ taskId: id }} className="underline">
            {tasks[index].data?.title ?? `Task ${id}`}
          </Link>
          {" · "}
          {tasks[index].data?.status === "completed"
            ? "Completed"
            : tasks[index].data
              ? "Waiting for completion"
              : "Status unavailable"}
        </li>
      ))}
    </ul>
  );
}
