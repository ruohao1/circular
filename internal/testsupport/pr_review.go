package testsupport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/prreviews"
)

// SeedPRReviewSource creates only owned fixture data; it never queues execution.
func SeedPRReviewSource(t *testing.T, pool *pgxpool.Pool, project uuid.UUID) prreviews.LaunchSnapshot {
	t.Helper()
	snapshot, err := SeedPRReviewSourceContext(t.Context(), pool, project)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// SeedPRReviewSourceContext is the same owned fixture for the disposable browser stack.
func SeedPRReviewSourceContext(ctx context.Context, pool *pgxpool.Pool, project uuid.UUID) (prreviews.LaunchSnapshot, error) {
	if project == uuid.Nil {
		project = uuid.New()
		if _, err := pool.Exec(ctx, `INSERT INTO projects(id,name) VALUES($1,'PR review fixture')`, project); err != nil {
			return prreviews.LaunchSnapshot{}, err
		}
	}
	repository, coder, task, run := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	defer tx.Rollback(ctx)
	reviewer, err := agents.EnsureReviewer(ctx, tx, project.String())
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO pr_review_settings(project_id,reviewer_id) VALUES($1,$2) ON CONFLICT(project_id) DO NOTHING`, project, reviewer)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1,$2,'fixture/private-source','https://github.com/fixture/private-source.git','main','{"github":{"repository_id":"101","installation_id":"42"}}')`, repository, project)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Fixture coder','codex','Implement the task','{}',true)`, coder, project)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	refs := json.RawMessage(`{"linear":{"issue_id":"20000000-0000-4000-8000-000000000003","account_id":"linear-workspace","url":"https://linear.app/fixture/issue/T-1"}}`)
	_, err = tx.Exec(ctx, `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) VALUES($1,$2,$3,'Fix the boundary','Zero must be accepted.','open',$4)`, task, project, repository, refs)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'codex','succeeded',1,'{}')`, run, task, coder)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	pr := prreviews.PRIdentity{InstallationID: "42", GitHubRepositoryID: "101", RepositoryName: "fixture/private-source", Number: 1, URL: "https://github.com/fixture/private-source/pull/1", BaseRef: "main", HeadRef: "circular/run/" + run.String(), BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
	_, err = tx.Exec(ctx, `INSERT INTO github_run_deliveries(run_id,project_id,repository_id,github_repository_id,installation_id,repository_name,base_branch,branch,title,status,base_commit,commit_sha,pull_request_number,pull_request_url) VALUES($1,$2,$3,'101','42','fixture/private-source','main',$4,'Fixture PR','delivered',$5,$6,1,$7)`, run, project, repository, pr.HeadRef, pr.BaseSHA, pr.HeadSHA, pr.URL)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	r := prreviews.ReviewerSnapshot{AgentID: uuid.MustParse(reviewer), Name: agents.ReviewerName, Backend: "codex", Instructions: agents.ReviewerInstructions, Model: "gpt-6-astra", ReasoningEffort: "low", BackendConfig: json.RawMessage(`{"model":"gpt-6-astra","reasoning_effort":"low"}`), PromptVersion: "pr-review-v1"}
	r.Fingerprint, err = prreviews.Fingerprint(r)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	snapshot := prreviews.LaunchSnapshot{SourceRunID: run, TaskID: task, ProjectID: project, RepositoryID: repository, CloneURL: "https://github.com/fixture/private-source.git", PR: pr, Reviewer: r, TaskTitle: "Fix the boundary", TaskDescription: "Zero must be accepted.", TaskExternalRefs: refs, Evidence: []prreviews.Evidence{}}
	snapshot.InputFingerprint, err = prreviews.Fingerprint(snapshot)
	if err != nil {
		return prreviews.LaunchSnapshot{}, err
	}
	return snapshot, nil
}
