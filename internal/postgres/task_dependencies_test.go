package postgres_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
)

func TestTaskPrerequisitesValidateAndSerialize(t *testing.T) {
	f := planningSeed(t)
	a, b, c := f.task(t, "A", "ready", "{}"), f.task(t, "B", "ready", "{}"), f.task(t, "C", "ready", "{}")
	foreignProject, foreign := uuid.New(), uuid.New()
	if _, err := f.p.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Foreign')`, foreignProject); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Exec(t.Context(), `INSERT INTO tasks(id,project_id,title,description,status,external_refs) VALUES($1,$2,'Foreign','','open','{}')`, foreign, foreignProject); err != nil {
		t.Fatal(err)
	}
	set := func(id uuid.UUID, ids ...uuid.UUID) error {
		r, _ := f.versions(t, id)
		_, err := f.patch(t, id, r, postgres.TaskPlanningPatch{PrerequisitesSet: true, PrerequisiteTaskIDs: ids})
		return err
	}
	for _, ids := range [][]uuid.UUID{{a}, {uuid.New()}, {foreign}, {b, b}, {uuid.Nil}} {
		if err := set(a, ids...); !errors.Is(err, postgres.ErrTaskPlanningInput) {
			t.Fatal("invalid edge accepted", ids, err)
		}
	}
	if err := set(a, b); err != nil {
		t.Fatal(err)
	}
	if err := set(b, c); err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]uuid.UUID{{b}, {a}} {
		if err := set(c, ids...); !errors.Is(err, postgres.ErrTaskPlanningInput) {
			t.Fatal("cycle accepted", err)
		}
	}
	x, y := f.task(t, "X", "ready", "{}"), f.task(t, "Y", "ready", "{}")
	start, results := make(chan struct{}), make(chan error, 2)
	for _, edge := range [][2]uuid.UUID{{x, y}, {y, x}} {
		go func() { <-start; results <- set(edge[0], edge[1]) }()
	}
	close(start)
	accepted := 0
	for range 2 {
		if err := <-results; err == nil {
			accepted++
		} else if !errors.Is(err, postgres.ErrTaskPlanningInput) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatal("opposing edges admitted a cycle", accepted)
	}
}

func TestTaskPrerequisitePatchSemantics(t *testing.T) {
	f := planningSeed(t)
	a, b, c := f.task(t, "A", "ready", "{}"), f.task(t, "B", "completed", "{}"), f.task(t, "C", "completed", "{}")
	r, board := f.versions(t, a)
	first, err := f.patch(t, a, r, postgres.TaskPlanningPatch{Title: strptr("Edited"), PrerequisitesSet: true, PrerequisiteTaskIDs: []uuid.UUID{c, b}})
	if err != nil || first.Revision != r+1 || first.BoardRevision != board+1 {
		t.Fatal(first, err)
	}
	for _, patch := range []postgres.TaskPlanningPatch{{}, {PrerequisitesSet: true, PrerequisiteTaskIDs: []uuid.UUID{b, c}}} {
		out, err := f.patch(t, a, first.Revision, patch)
		if err != nil || out.Changed || out.Revision != first.Revision || out.BoardRevision != first.BoardRevision {
			t.Fatal("no-op changed revisions", out, err)
		}
	}
	if _, err := f.patch(t, a, r, postgres.TaskPlanningPatch{PrerequisitesSet: true}); !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("stale edit", err)
	}
	tx, err := f.p.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ids, err := postgres.TaskPrerequisites(t.Context(), tx, a)
	tx.Rollback(t.Context())
	if err != nil || len(ids) != 2 || !slices.Contains(ids, b) || !slices.Contains(ids, c) {
		t.Fatal(ids, err)
	}
	_, err = f.p.Exec(t.Context(), `INSERT INTO work_sessions(id,project_id,task_id,kind,secret_digest,admission_digest,client_name,admitted_revision,admitted_prerequisites,claimed_at) VALUES($1,$2,$3,'external',decode(repeat('ab',32),'hex'),decode(repeat('cd',32),'hex'),'External',1,'[]',now())`, uuid.New(), f.project, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.patch(t, a, first.Revision, postgres.TaskPlanningPatch{PrerequisitesSet: true}); !errors.Is(err, postgres.ErrTaskPlanningConflict) {
		t.Fatal("current owner dependencies changed", err)
	}
	if _, err = f.p.Exec(t.Context(), `UPDATE work_sessions SET released_at=now(),release_reason='Done' WHERE task_id=$1`, a); err != nil {
		t.Fatal(err)
	}
	out, err := f.patch(t, a, first.Revision, postgres.TaskPlanningPatch{PrerequisitesSet: true, PrerequisiteTaskIDs: []uuid.UUID{}})
	if err != nil || !out.Changed {
		t.Fatal("clear failed", out, err)
	}
}
