package migrate_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/migrate"
	"testing"
)

func TestCompletionUpgrade(t *testing.T) {
	t.Run("preserves history", func(t *testing.T) {
		p := legacyDatabase(t, 19)
		_, task, _ := workHistory(t, p)
		var before, after []byte
		if err := p.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM tasks t WHERE id=$1`, task).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if err := migrate.Up(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		if err := p.QueryRow(t.Context(), `SELECT to_jsonb(t)-'completion_policy'-'completion_contract'-'completion_session_id'-'approval_context' FROM tasks t WHERE id=$1`, task).Scan(&after); err != nil || string(before) != string(after) {
			t.Fatal("history changed", err)
		}
		var untouched bool
		if err := p.QueryRow(t.Context(), `SELECT completion_policy IS NULL AND completion_contract IS NULL AND completion_session_id IS NULL FROM tasks WHERE id=$1`, task).Scan(&untouched); err != nil || !untouched {
			t.Fatal("invented completion contract", err)
		}
	})
	for _, which := range []string{"owner", "worker", "workspace"} {
		t.Run(which, func(t *testing.T) {
			p := legacyDatabase(t, 19)
			project, task, _ := workHistory(t, p)
			var err error
			switch which {
			case "owner":
				_, err = p.Exec(t.Context(), `INSERT INTO work_sessions(id,project_id,task_id,kind,secret_digest,admission_digest,client_name,admitted_revision,admitted_prerequisites,claimed_at) VALUES($1,$2,$3,'external',decode(repeat('01',32),'hex'),decode(repeat('02',32),'hex'),'Codex',1,'[]',now())`, uuid.New(), project, task)
			case "worker":
				_, err = p.Exec(t.Context(), `UPDATE runs SET worker_id='old-worker',lease_expires_at=now()`)
			case "workspace":
				_, err = p.Exec(t.Context(), `UPDATE workspaces SET status='ready'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := migrate.Up(t.Context(), p); err == nil {
				t.Fatal("upgrade accepted unsafe work")
			}
			var version string
			if err := p.QueryRow(t.Context(), `SELECT version_num FROM alembic_version`).Scan(&version); err != nil || version != "0019" {
				t.Fatal("partial migration", version, err)
			}
		})
	}
}
