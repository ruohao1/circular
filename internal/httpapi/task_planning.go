package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
)

// Decode fields individually so omission, null, and duplicate keys cannot be
// confused. This contract is intentionally stricter than legacy resource create.
func planningBody(w http.ResponseWriter, r *http.Request, allowed ...string) (map[string]json.RawMessage, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		problem(w, 413, "request body exceeds 128 KiB")
		return nil, false
	}
	bad := func() (map[string]json.RawMessage, bool) {
		invalid(w, "body", "", "json_invalid", "One JSON object with unique, known fields is required")
		return nil, false
	}
	if !utf8.Valid(data) {
		return bad()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return bad()
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		name, ok := token.(string)
		if err != nil || !ok || !slices.Contains(allowed, name) || fields[name] != nil {
			return bad()
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return bad()
		}
		fields[name] = raw
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return bad()
	}
	if _, err = d.Token(); err != io.EOF {
		return bad()
	}
	return fields, true
}
func planningRevision(w http.ResponseWriter, fields map[string]json.RawMessage, name string) (int64, bool) {
	var n int64
	if json.Unmarshal(fields[name], &n) != nil || n < 1 || n > 9007199254740991 {
		invalid(w, "body", name, "value_error", "A positive safe integer revision is required")
		return 0, false
	}
	return n, true
}
func planningText(w http.ResponseWriter, fields map[string]json.RawMessage, name string) (*string, bool) {
	raw, exists := fields[name]
	if !exists {
		return nil, true
	}
	var s string
	if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &s) != nil || strings.ContainsRune(s, 0) {
		invalid(w, "body", name, "string_type", "Valid text is required")
		return nil, false
	}
	return &s, true
}
func planningUUID(w http.ResponseWriter, fields map[string]json.RawMessage, name string) (*uuid.UUID, bool) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(raw, []byte("null")) {
		return nil, true
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		invalid(w, "body", name, "uuid_parsing", "UUID or null required")
		return nil, false
	}
	id, ok := identifier(w, s, "body", name)
	return &id, ok
}
func decodeTaskPlanningUpdate(w http.ResponseWriter, r *http.Request) (int64, postgres.TaskPlanningPatch, bool) {
	var p postgres.TaskPlanningPatch
	fields, ok := planningBody(w, r, "expected_revision", "expected_board_revision", "title", "description", "priority", "repository_id", "preferred_agent_id", "planned_start_date", "planned_finish_date", "prerequisite_task_ids")
	if !ok {
		return 0, p, false
	}
	if raw, present := fields["prerequisite_task_ids"]; present {
		p.PrerequisitesSet = true
		if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &p.PrerequisiteTaskIDs) != nil {
			invalid(w, "body", "prerequisite_task_ids", "value_error", "An array of Task UUIDs is required")
			return 0, p, false
		}
		for _, id := range p.PrerequisiteTaskIDs {
			if id == uuid.Nil {
				invalid(w, "body", "prerequisite_task_ids", "value_error", "Nonzero Task UUIDs are required")
				return 0, p, false
			}
		}
	}
	_, p.PlannedStartSet = fields["planned_start_date"]
	_, p.PlannedFinishSet = fields["planned_finish_date"]
	if _, present := fields["expected_board_revision"]; present || p.PlannedStartSet || p.PlannedFinishSet {
		board, valid := planningRevision(w, fields, "expected_board_revision")
		if !valid {
			return 0, p, false
		}
		p.ExpectedBoardRevision = &board
	}
	for _, field := range []struct {
		name  string
		value **string
	}{
		{"planned_start_date", &p.PlannedStartDate}, {"planned_finish_date", &p.PlannedFinishDate},
	} {
		if raw, present := fields[field.name]; present && !bytes.Equal(raw, []byte("null")) {
			if *field.value, ok = planningText(w, fields, field.name); !ok {
				return 0, p, false
			}
		}
	}
	if err := postgres.ValidateTaskSchedule(p.PlannedStartDate, p.PlannedFinishDate); err != nil {
		invalid(w, "body", "planned_start_date", "value_error", err.Error())
		return 0, p, false
	}
	revision, ok := planningRevision(w, fields, "expected_revision")
	if !ok {
		return 0, p, false
	}
	if p.Title, ok = planningText(w, fields, "title"); !ok {
		return 0, p, false
	}
	if p.Title != nil {
		s := strings.TrimSpace(*p.Title)
		if s == "" || utf8.RuneCountInString(s) > 500 {
			invalid(w, "body", "title", "value_error", "Title must contain 1–500 characters")
			return 0, p, false
		}
		p.Title = &s
	}
	if p.Description, ok = planningText(w, fields, "description"); !ok {
		return 0, p, false
	}
	if p.Description != nil && utf8.RuneCountInString(*p.Description) > 20000 {
		invalid(w, "body", "description", "string_too_long", "Description must contain at most 20000 characters")
		return 0, p, false
	}
	if p.Priority, ok = planningText(w, fields, "priority"); !ok {
		return 0, p, false
	}
	if p.Priority != nil && !postgres.ValidTaskPriority(*p.Priority) {
		invalid(w, "body", "priority", "enum", "Choose none, low, normal, high, or urgent")
		return 0, p, false
	}
	_, p.RepositorySet = fields["repository_id"]
	_, p.PreferredAgentSet = fields["preferred_agent_id"]
	if p.RepositoryID, ok = planningUUID(w, fields, "repository_id"); !ok {
		return 0, p, false
	}
	if p.PreferredAgentID, ok = planningUUID(w, fields, "preferred_agent_id"); !ok {
		return 0, p, false
	}
	return revision, p, true
}
func decodeTaskMove(w http.ResponseWriter, r *http.Request) (postgres.TaskMoveInput, bool) {
	var in postgres.TaskMoveInput
	fields, ok := planningBody(w, r, "expected_revision", "expected_board_revision", "status", "placement", "anchor_task_id")
	if !ok {
		return in, false
	}
	if in.ExpectedRevision, ok = planningRevision(w, fields, "expected_revision"); !ok {
		return in, false
	}
	if in.ExpectedBoardRevision, ok = planningRevision(w, fields, "expected_board_revision"); !ok {
		return in, false
	}
	status, ok := planningText(w, fields, "status")
	if !ok {
		return in, false
	}
	placement, ok := planningText(w, fields, "placement")
	if !ok {
		return in, false
	}
	if status == nil || !postgres.ValidTaskStatus(*status) || placement == nil || !slices.Contains([]string{"start", "end", "before", "after", "up", "down"}, *placement) {
		invalid(w, "body", "", "enum", "Supported status and placement are required")
		return in, false
	}
	in.Status, in.Placement = *status, *placement
	_, present := fields["anchor_task_id"]
	anchored := in.Placement == "before" || in.Placement == "after"
	if in.AnchorID, ok = planningUUID(w, fields, "anchor_task_id"); !ok {
		return in, false
	}
	if anchored && in.AnchorID == nil || !anchored && present {
		invalid(w, "body", "anchor_task_id", "value_error", "Anchor is required only for before/after")
		return in, false
	}
	return in, true
}
func planningError(w http.ResponseWriter, err error) bool {
	var completionConflict *postgres.CompletionConflict
	if errors.As(err, &completionConflict) {
		respond(w, 409, map[string]any{"detail": err.Error(), "code": "completion_blocked", "blockers": completionConflict.Blockers})
		return true
	}
	if errors.Is(err, postgres.ErrTaskPlanningInput) {
		problem(w, 422, err.Error())
		return true
	}
	if errors.Is(err, postgres.ErrTaskPlanningConflict) {
		problem(w, 409, err.Error())
		return true
	}
	return dbError(w, err, "Task")
}
func (a *api) updateTaskPlanning(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("task_id"), "path", "task_id")
	if !ok {
		return
	}
	revision, patch, ok := decodeTaskPlanningUpdate(w, r)
	if !ok {
		return
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Task") {
		return
	}
	defer rollback(r.Context(), tx)
	out, err := postgres.UpdateTaskPlanning(r.Context(), tx, id, revision, patch)
	if planningError(w, err) {
		return
	}
	task, err := record(r.Context(), tx, "tasks", "TaskRead", "WHERE t.id=$1", id)
	if dbError(w, err, "Task") {
		return
	}
	if dbError(w, tx.Commit(r.Context()), "Task") {
		return
	}
	respond(w, 200, map[string]any{"task": task, "board_revision": out.BoardRevision})
}
func (a *api) moveTask(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("task_id"), "path", "task_id")
	if !ok {
		return
	}
	input, ok := decodeTaskMove(w, r)
	if !ok {
		return
	}
	if input.Status == "completed" {
		if _, err := a.integrations.RefreshTaskCompletion(r.Context(), id); completionError(w, err) {
			return
		}
	}
	tx, err := a.pool.Begin(r.Context())
	if dbError(w, err, "Task") {
		return
	}
	defer rollback(r.Context(), tx)
	out, err := postgres.MoveTask(r.Context(), tx, id, input)
	if planningError(w, err) {
		return
	}
	task, err := record(r.Context(), tx, "tasks", "TaskRead", "WHERE t.id=$1", id)
	if dbError(w, err, "Task") {
		return
	}
	if dbError(w, tx.Commit(r.Context()), "Task") {
		return
	}
	respond(w, 200, map[string]any{"task": task, "board_revision": out.BoardRevision})
}
