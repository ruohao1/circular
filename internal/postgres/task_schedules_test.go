package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
)

func scheduleDates(t *testing.T, f planningFixture, id uuid.UUID) (start, finish *string, updated time.Time) {
	t.Helper()
	if err := f.p.QueryRow(t.Context(), `SELECT to_char(planned_start_date,'YYYY-MM-DD'),to_char(planned_finish_date,'YYYY-MM-DD'),updated_at FROM tasks WHERE id=$1`, id).Scan(&start, &finish, &updated); err != nil {
		t.Fatal(err)
	}
	return
}

func TestTaskSchedulePatchPresenceAndDates(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Dates", "open", "{}")
	for _, value := range []string{"0001-01-01", "0099-12-31", "2024-02-29", "2026-03-29", "2026-11-01", "9999-12-31"} {
		r, b := f.versions(t, id)
		_, err := f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true, PlannedFinishSet: true, PlannedStartDate: &value, PlannedFinishDate: &value})
		if err != nil {
			t.Fatal(value, err)
		}
		start, finish, _ := scheduleDates(t, f, id)
		if start == nil || finish == nil || *start != value || *finish != value {
			t.Fatal(value, start, finish)
		}
	}
	for _, value := range []string{"", "0000-01-01", "10000-01-01", "2026-02-29", "2024-02-30", "2026-13-01", "2026-01-00", "2026-1-01", "2026-10-04T00:00:00Z", "2026-10-04 ", "infinity", "-infinity"} {
		r, b := f.versions(t, id)
		_, err := f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true, PlannedStartDate: &value})
		if !errors.Is(err, postgres.ErrTaskPlanningInput) {
			t.Errorf("date %q accepted: %v", value, err)
		}
	}
	r, b := f.versions(t, id)
	out, err := f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true})
	if err != nil {
		t.Fatal(err)
	}
	start, finish, _ := scheduleDates(t, f, id)
	if start != nil || finish == nil || *finish != "9999-12-31" {
		t.Fatal("one-sided clear", start, finish)
	}
	_, err = f.patch(t, id, out.Revision, postgres.TaskPlanningPatch{Priority: strptr("high")})
	if err != nil {
		t.Fatal("old-only patch", err)
	}
	r, b = f.versions(t, id)
	_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true, PlannedFinishSet: true, PlannedStartDate: strptr("2026-10-04"), PlannedFinishDate: strptr("2026-10-06")})
	if err != nil {
		t.Fatal(err)
	}
	r, b = f.versions(t, id)
	_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true, PlannedStartDate: strptr("2026-10-07")})
	if !errors.Is(err, postgres.ErrTaskPlanningInput) {
		t.Fatal("one-sided invalid combined range", err)
	}
	start, finish, _ = scheduleDates(t, f, id)
	if *start != "2026-10-04" || *finish != "2026-10-06" {
		t.Fatal("invalid edit changed dates")
	}
	_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{PlannedFinishSet: true})
	if !errors.Is(err, postgres.ErrTaskPlanningInput) {
		t.Fatal("missing board revision", err)
	}
}

func TestTaskScheduleNoopAndRevisions(t *testing.T) {
	f := planningSeed(t)
	id := f.task(t, "Original", "open", "{}")
	r, b := f.versions(t, id)
	out, err := f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, Title: strptr("Scheduled"), PlannedStartSet: true, PlannedFinishSet: true, PlannedStartDate: strptr("2026-10-04"), PlannedFinishDate: strptr("2026-10-04")})
	if err != nil || !out.Changed || out.Revision != r+1 || out.BoardRevision != b+1 {
		t.Fatal(out, err)
	}
	_, _, stamp := scheduleDates(t, f, id)
	r, b = f.versions(t, id)
	noop := postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true, PlannedStartDate: strptr("2026-10-04")}
	out, err = f.patch(t, id, r, noop)
	_, _, after := scheduleDates(t, f, id)
	if err != nil || out.Changed || out.Revision != r || out.BoardRevision != b || !after.Equal(stamp) {
		t.Fatal("noop changed versions/timestamp", out, err, stamp, after)
	}
	_, err = f.patch(t, id, r-1, noop)
	if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("stale no-op", err)
	}
	f.task(t, "Concurrent", "open", "{}")
	_, err = f.patch(t, id, r, noop)
	if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("stale board no-op", err)
	}
	_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, Title: strptr("Scheduled")})
	if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("old-only optional board not checked", err)
	}
	for _, bad := range []int64{0, -1, 9007199254740992} {
		_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &bad})
		if !errors.Is(err, postgres.ErrTaskPlanningInput) {
			t.Fatal("invalid optional board", bad, err)
		}
	}
	for _, which := range []string{"task", "project"} {
		t.Run(which+" overflow", func(t *testing.T) {
			f := planningSeed(t)
			id := f.task(t, "Original", "open", "{}")
			if which == "task" {
				_, err = f.p.Exec(t.Context(), `UPDATE tasks SET revision=9007199254740991 WHERE id=$1`, id)
			} else {
				_, err = f.p.Exec(t.Context(), `UPDATE projects SET task_board_revision=9007199254740991 WHERE id=$1`, f.project)
			}
			if err != nil {
				t.Fatal(err)
			}
			r, b := f.versions(t, id)
			_, _, stamp := scheduleDates(t, f, id)
			_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true, PlannedStartDate: strptr("2026-10-04")})
			if err == nil {
				t.Fatal("overflow accepted")
			}
			start, finish, after := scheduleDates(t, f, id)
			gotR, gotB := f.versions(t, id)
			if start != nil || finish != nil || gotR != r || gotB != b || !after.Equal(stamp) {
				t.Fatal("overflow did not roll back")
			}
		})
	}
}

func TestTaskScheduleContentLockIndependence(t *testing.T) {
	for _, kind := range []string{"linear", "discovery", "queued", "provisioning", "running", "waiting_for_approval", "waiting_for_input", "finalizing"} {
		t.Run(kind, func(t *testing.T) {
			f := planningSeed(t)
			refs := "{}"
			if kind == "linear" {
				refs = `{"linear":{"issue_id":"source"}}`
			} else if kind == "discovery" {
				refs = `{"circular":{"kind":"repository-discovery"}}`
			}
			id := f.task(t, "Keep instructions", "open", refs)
			if refs == "{}" {
				if _, err := f.p.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($1,$2,$3,'fake',$4,1,'{}')`, uuid.New(), id, f.agent, kind); err != nil {
					t.Fatal(err)
				}
			}
			r, b := f.versions(t, id)
			out, err := f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, PlannedStartSet: true, PlannedStartDate: strptr("2026-10-04")})
			if err != nil || !out.Changed {
				t.Fatal("dates locked with content", out, err)
			}
			r, b = f.versions(t, id)
			_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, Title: strptr("Must not save"), PlannedFinishSet: true, PlannedFinishDate: strptr("2026-10-06")})
			if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
				t.Fatal("mixed content edit allowed", err)
			}
			_, err = f.patch(t, id, r, postgres.TaskPlanningPatch{ExpectedBoardRevision: &b, RepositorySet: true, PlannedFinishSet: true, PlannedFinishDate: strptr("2026-10-06")})
			if !errors.Is(err, postgres.ErrTaskPlanningConflict) {
				t.Fatal("mixed Repository edit allowed", err)
			}
			start, finish, _ := scheduleDates(t, f, id)
			gotR, gotB := f.versions(t, id)
			if start == nil || *start != "2026-10-04" || finish != nil || r != gotR || b != gotB {
				t.Fatal("mixed patch partially committed")
			}
			var count int
			if err = f.p.QueryRow(t.Context(), `SELECT count(*) FROM runs WHERE task_id=$1 AND status=$2`, id, kind).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if refs == "{}" && count != 1 {
				t.Fatal("schedule changed Run", count)
			}
		})
	}
}
