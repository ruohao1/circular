package migrate_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/migrate"
)

func TestTaskScheduleMigrationPreservesHistory(t *testing.T) {
	for _, version := range []int{15, 16} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			p := legacyDatabase(t, version)
			project, task, agent, run := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			for _, step := range []struct {
				sql  string
				args []any
			}{
				{`INSERT INTO projects(id,name) VALUES($1,'Schedules')`, []any{project}},
				{`INSERT INTO tasks(id,project_id,title,description,status,external_refs,created_at,updated_at) VALUES($1,$2,'Keep title','Keep description','completed','{"keep":true}',$3,$3)`, []any{task, project, stamp}},
				{`INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($1,$2,'Engineer','fake','','{}',true)`, []any{agent, project}},
				{`INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake','succeeded',1,'{}')`, []any{run, task, agent}},
			} {
				if _, err := p.Exec(t.Context(), step.sql, step.args...); err != nil {
					t.Fatal(err)
				}
			}
			if version == 16 {
				if _, err := p.Exec(t.Context(), `UPDATE tasks SET revision=7,board_position=4,priority='high' WHERE id=$1`, task); err != nil {
					t.Fatal(err)
				}
				if _, err := p.Exec(t.Context(), `UPDATE projects SET task_board_revision=23 WHERE id=$1`, project); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := migrate.Up(t.Context(), p); err != nil {
					t.Fatal(err)
				}
			}
			var start, finish *string
			var title, desc, status, priority string
			var revision, board, position int64
			var updated, created time.Time
			var keep bool
			err := p.QueryRow(t.Context(), `SELECT t.planned_start_date::text,t.planned_finish_date::text,t.title,t.description,t.status,t.priority,t.revision,p.task_board_revision,t.board_position,t.created_at,t.updated_at,(t.external_refs->>'keep')::bool FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=$1`, task).Scan(&start, &finish, &title, &desc, &status, &priority, &revision, &board, &position, &created, &updated, &keep)
			if err != nil {
				t.Fatal(err)
			}
			if start != nil || finish != nil || title != "Keep title" || desc != "Keep description" || status != "completed" || !keep || !created.Equal(stamp) || !updated.Equal(stamp) {
				t.Fatal("migration changed historical Task", start, finish, title, desc, status, keep, created, updated)
			}
			if version == 16 && (revision != 7 || board != 23 || position != 4 || priority != "high") {
				t.Fatal("migration reset planning", revision, board, position, priority)
			}
			var count int
			if err = p.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE id=$1 AND status='succeeded'`, run).Scan(&count); err != nil || count != 1 {
				t.Fatal(count, err)
			}
			for _, dates := range [][2]string{{"2026-10-05", "2026-10-04"}, {"infinity", "infinity"}, {"-infinity", "2026-10-04"}, {"0001-01-01 BC", "0001-01-01"}, {"9999-12-31", "10000-01-01"}, {"2026-02-29", "2026-03-01"}} {
				if _, err = p.Exec(t.Context(), `UPDATE tasks SET planned_start_date=$2::text::date,planned_finish_date=$3::text::date WHERE id=$1`, task, dates[0], dates[1]); err == nil {
					t.Errorf("stored invalid dates %v", dates)
				}
			}
			for _, date := range []string{"0001-01-01", "0099-12-31", "2024-02-29", "9999-12-31"} {
				if _, err = p.Exec(t.Context(), `UPDATE tasks SET planned_start_date=$2::text::date,planned_finish_date=$2::text::date WHERE id=$1`, task, date); err != nil {
					t.Fatal(date, err)
				}
			}
		})
	}
}
