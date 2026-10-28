package testsupport

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/agents"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

// SeedExternalPRReviewSource admits and finishes real tracked external fixture work.
func SeedExternalPRReviewSource(t *testing.T, pool *pgxpool.Pool, project uuid.UUID) prreviews.LaunchSnapshot {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if project == uuid.Nil {
		project = uuid.New()
		if _, err = tx.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'External review fixture')`, project); err != nil {
			t.Fatal(err)
		}
	}
	reviewer, err := agents.EnsureReviewer(t.Context(), tx, project.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO pr_review_settings(project_id,reviewer_id) VALUES($1,$2) ON CONFLICT(project_id) DO NOTHING`, project, reviewer); err != nil {
		t.Fatal(err)
	}
	repo, task, session := uuid.New(), uuid.New(), uuid.New()
	if _, err = tx.Exec(t.Context(), `INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs,completion_check_config) VALUES($1,$2,'fixture/private-source','https://github.com/fixture/private-source.git','main','{"github":{"repository_id":"101","installation_id":"42"}}','{"revision":1,"checks":[{"workflow_id":"123","app_id":"42","workflow_path":".github/workflows/ci.yml","name":"Tests"}]}')`, repo, project); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs,completion_policy) VALUES($1,$2,$3,'External change','Verify the boundary','ready','{}','{"mode":"review_required","requirements":[]}')`, task, project, repo); err != nil {
		t.Fatal(err)
	}
	ApproveTaskTx(t, tx, task)
	now := time.Now().UTC()
	secret := bytes.Repeat([]byte{19}, 32)
	if _, err = postgres.ClaimExternalWork(t.Context(), tx, postgres.ExternalWorkClaim{TaskID: task, SessionID: session, ExpectedRevision: 1, Secret: secret, ClientName: "External fixture"}, now); err != nil {
		t.Fatal(err)
	}
	number := 1
	if _, err = postgres.FinishExternalWork(t.Context(), tx, postgres.WorkFinishInput{Credential: postgres.WorkCredential{SessionID: session, Secret: secret}, OperationID: uuid.New(), Outcome: "succeeded", Report: &completion.Report{Result: "External agent result", Evidence: []completion.Evidence{}, PullRequestNumber: &number}}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	snap, err := postgres.NewPRReviewStore(pool).SourceForWork(t.Context(), session, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	snap.PR.BaseSHA = strings.Repeat("a", 40)
	snap.PR.HeadSHA = strings.Repeat("b", 40)
	snap.PR.BaseRef = "main"
	snap.PR.HeadRef = "external/change"
	snap.PR.URL = "https://github.com/fixture/private-source/pull/1"
	snap, err = prreviews.SealSnapshot(snap)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
