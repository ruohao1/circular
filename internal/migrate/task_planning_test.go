package migrate_test

import (
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/migrate"
	"testing"
	"time"
)

func TestTaskPlanningMigrationPreservesHistory(t *testing.T) {
	p := legacyDatabase(t, 15)
	project, agent := uuid.New(), uuid.New()
	if _, e := p.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Planning')`, project); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Exec(t.Context(), `INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($2,$1,'Engineer','fake','','{}',true)`, project, agent); e != nil {
		t.Fatal(e)
	}
	stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	ids := []uuid.UUID{uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002"), uuid.New(), uuid.New(), uuid.New()}
	statuses := []string{"open", "open", "in_progress", "completed", "cancelled"}
	for i, id := range ids {
		if _, e := p.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs,created_at,updated_at) VALUES($1,$2,'Keep title','Keep description',$3,'{"keep":true}',$4,$4)`, id, project, statuses[i], stamp); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := p.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake','succeeded',1,'{}')`, uuid.New(), ids[0], agent); e != nil {
		t.Fatal(e)
	}
	for range 2 {
		if e := migrate.Up(t.Context(), p); e != nil {
			t.Fatal(e)
		}
	}
	for i, id := range ids {
		var title, desc, status, priority string
		var keep bool
		var preferred *uuid.UUID
		var revision, pos int64
		var created, updated time.Time
		e := p.QueryRow(t.Context(), `SELECT title,description,status,priority,(external_refs->>'keep')::bool,preferred_agent_id,revision,board_position,created_at,updated_at FROM tasks WHERE id=$1`, id).Scan(&title, &desc, &status, &priority, &keep, &preferred, &revision, &pos, &created, &updated)
		wantPos := int64(0)
		if i == 0 {
			wantPos = 1
		}
		if e != nil || title != "Keep title" || desc != "Keep description" || status != statuses[i] || priority != "none" || !keep || preferred != nil || revision != 1 || pos != wantPos || !created.Equal(stamp) || !updated.Equal(stamp) {
			t.Fatalf("history changed: %s %s %s %s %v %v %d %d %v %v %v", title, desc, status, priority, keep, preferred, revision, pos, created, updated, e)
		}
	}
	var count int
	if e := p.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE task_id=$1 AND status='succeeded'`, ids[0]).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}
