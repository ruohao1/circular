package httpapi_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestTaskParentCreateAndImmutability(t *testing.T) {
	f := setup(t)
	project, _, parent := queueFixture(t, f, "Parent", true)
	child := f.create(t, "tasks", map[string]any{"project_id": project, "title": "Child", "parent_task_id": parent})
	if child["parent_task_id"] != parent {
		t.Fatal("parent omitted", child)
	}
	var read map[string]any
	if err := json.Unmarshal(f.request(t, "GET", "/api/v1/tasks/"+child["id"].(string), "", 200), &read); err != nil || read["parent_task_id"] != parent {
		t.Fatal(read, err)
	}
	_, _, foreign := queueFixture(t, f, "Foreign", true)
	for _, id := range []string{foreign, uuid.NewString()} {
		f.request(t, "POST", "/api/v1/tasks", fmt.Sprintf(`{"project_id":%q,"title":"Invalid","parent_task_id":%q}`, project, id), 404)
	}
	f.request(t, "PATCH", "/api/v1/tasks/"+child["id"].(string), `{"expected_revision":1,"parent_task_id":null}`, 422)
	if _, err := f.pool.Exec(t.Context(), `UPDATE tasks SET parent_task_id=NULL WHERE id=$1`, child["id"]); err == nil {
		t.Fatal("parent changed through SQL")
	}
}

func TestTaskPrerequisitePatchSemantics(t *testing.T) {
	f := setup(t)
	project, _, task := queueFixture(t, f, "Dependent", true)
	other := f.create(t, "tasks", map[string]any{"project_id": project, "title": "Prerequisite"})
	path := "/api/v1/tasks/" + task
	for _, value := range []string{"null", `["bad"]`, `[null]`, `["00000000-0000-0000-0000-000000000000"]`} {
		f.request(t, "PATCH", path, `{"expected_revision":1,"prerequisite_task_ids":`+value+`}`, 422)
	}
	f.approvePlanning(t, path, fmt.Sprintf(`{"expected_revision":1,"prerequisite_task_ids":[%q]}`, other["id"]))
	f.request(t, "PATCH", path, `{"expected_revision":1,"prerequisite_task_ids":[]}`, 409)
	var detail struct {
		IDs      []string `json:"prerequisite_task_ids"`
		Editable bool     `json:"dependencies_editable"`
	}
	if err := json.Unmarshal(f.request(t, "GET", path+"/planning", "", 200), &detail); err != nil || len(detail.IDs) != 1 || detail.IDs[0] != other["id"] || !detail.Editable {
		t.Fatal(detail, err)
	}
	f.approvePlanning(t, path, `{"expected_revision":2,"prerequisite_task_ids":[]}`)
}
