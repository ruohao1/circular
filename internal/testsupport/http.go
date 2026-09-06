package testsupport

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/httpapi"
)

type Event struct {
	ID       uuid.UUID      `json:"id"`
	RunID    uuid.UUID      `json:"run_id"`
	Sequence int64          `json:"sequence"`
	Type     string         `json:"type"`
	Source   string         `json:"source"`
	Data     map[string]any `json:"data"`
	Raw      map[string]any `json:"raw"`
}
type Snapshot struct {
	Run struct {
		ID         uuid.UUID  `json:"id"`
		Status     string     `json:"status"`
		Error      *string    `json:"error"`
		WorkerID   *string    `json:"worker_id"`
		StartedAt  *time.Time `json:"started_at"`
		FinishedAt *time.Time `json:"finished_at"`
	} `json:"run"`
	Workspace *struct {
		ID          uuid.UUID `json:"id"`
		Status      string    `json:"status"`
		ContainerID *string   `json:"container_id"`
	} `json:"workspace"`
	Artifacts []struct {
		ID       uuid.UUID      `json:"id"`
		Kind     string         `json:"kind"`
		URI      string         `json:"uri"`
		Metadata map[string]any `json:"metadata"`
	} `json:"artifacts"`
	Usage struct {
		InputTokens  json.Number `json:"input_tokens"`
		OutputTokens json.Number `json:"output_tokens"`
	} `json:"usage"`
	LastEventSequence int64   `json:"last_event_sequence"`
	Events            []Event `json:"-"`
}

// Observe reads through the production HTTP interface, including released claims
// that the execution-owned resource interface intentionally cannot access.
func Observe(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) Snapshot {
	t.Helper()
	h, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	read := func(path string, target any) {
		t.Helper()
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("HTTP observation: %d %s", response.Code, response.Body.String())
		}
		decoder := json.NewDecoder(response.Body)
		decoder.UseNumber()
		if err := decoder.Decode(target); err != nil {
			t.Fatal(err)
		}
	}
	var snapshot Snapshot
	read("/api/v1/runs/"+id.String()+"/execution", &snapshot)
	read("/api/v1/runs/"+id.String()+"/events?limit=1000", &snapshot.Events)
	return snapshot
}

func Cancel(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	h, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest("POST", "/api/v1/runs/"+id.String()+"/cancel", strings.NewReader("")))
	if response.Code != 200 {
		t.Fatalf("API cancellation: %d %s", response.Code, response.Body.String())
	}
}

func (s Snapshot) Types() []string {
	types := make([]string, len(s.Events))
	for i, event := range s.Events {
		types[i] = event.Type
	}
	return types
}
func (s Snapshot) Count(kind string) int {
	count := 0
	for _, event := range s.Events {
		if event.Type == kind {
			count++
		}
	}
	return count
}

func (s Snapshot) AssertReplay(t *testing.T) {
	t.Helper()
	for i, event := range s.Events {
		if event.RunID != s.Run.ID || event.Sequence != int64(i+1) {
			t.Fatalf("replay crossed Runs or lost ordering: %+v", event)
		}
	}
	if s.LastEventSequence != int64(len(s.Events)) {
		t.Fatal("execution cursor does not match persisted replay")
	}
}

func AssertJSON(t *testing.T, actual, expected any) {
	t.Helper()
	encode := func(value any) string {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if a, b := encode(actual), encode(expected); a != b {
		t.Fatalf("JSON mismatch:\nactual: %s\nexpected: %s", a, b)
	}
}

func (s Snapshot) AssertTypes(t *testing.T, expected ...string) {
	t.Helper()
	s.AssertReplay(t)
	if !reflect.DeepEqual(s.Types(), expected) {
		t.Fatalf("event types: %v; expected: %v", s.Types(), expected)
	}
}
