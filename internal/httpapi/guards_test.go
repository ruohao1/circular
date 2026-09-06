package httpapi_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/httpapi"
)

func TestCORSAllowsOnlyConfiguredOriginsAndStandardMethods(t *testing.T) {
	pool, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h, err := httpapi.New(pool, httpapi.Config{ArtifactRoot: t.TempDir(), CORSOrigins: []string{"http://localhost:5173"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		origin, method string
		status         int
	}{{"http://localhost:5173", "POST", 200}, {"https://foreign.invalid", "POST", 400}, {"http://localhost:5173", "TRACE", 400}} {
		r := httptest.NewRequest("OPTIONS", "/api/v1/projects", nil)
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Access-Control-Request-Method", test.method)
		r.Header.Set("Access-Control-Request-Headers", "content-type")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatal(w.Code, w.Body.String())
		}
		if test.status == 200 && (w.Header().Get("Access-Control-Allow-Origin") != test.origin || w.Header().Get("Access-Control-Allow-Headers") != "content-type") {
			t.Fatal(w.Header())
		}
		if test.origin == "https://foreign.invalid" && w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("foreign origin authorized")
		}
	}
}

func TestValidationCoversResourceBoundsAndDoesNotEchoPayload(t *testing.T) {
	f := setup(t)
	for _, body := range []string{`{"name":12}`, `{"name":true}`, `{"name":"secret\u0000value"}`, `{"name":"` + strings.Repeat("界", 201) + `"}`, `{"name":"secret"} {}`, string([]byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', '}'})} {
		response := f.request(t, "POST", "/api/v1/projects", body, 422)
		if strings.Contains(string(response), "secret") {
			t.Fatal("validation echoed input")
		}
	}
	f.create(t, "projects", map[string]any{"name": strings.Repeat("界", 200)})
	f.request(t, "POST", "/api/v1/projects", `{"name":"`+strings.Repeat("x", 16*1024*1024)+`"}`, 413)
	for _, kind := range []string{"repositories", "agents", "tasks", "runs"} {
		f.request(t, "GET", "/api/v1/"+kind+"?project_id=invalid", "", 422)
	}
	f.request(t, "POST", "/api/v1/agents", `{"project_id":"`+uuid.NewString()+`","name":"missing project"}`, 404)
}

func TestCancellationEventFailureRollsBackRunAndRetrySucceeds(t *testing.T) {
	f := setup(t)
	id := f.run(t)["id"].(string)
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE events ADD CONSTRAINT reject_cancel CHECK(type<>'run.cancelled')`); err != nil {
		t.Fatal(err)
	}
	response := f.request(t, "POST", "/api/v1/runs/"+id+"/cancel", "", 500)
	if strings.Contains(string(response), "reject_cancel") {
		t.Fatal("database diagnostic escaped")
	}
	s := decode(t, f.request(t, "GET", "/api/v1/runs/"+id, "", 200))
	if s["status"] != "queued" || s["finished_at"] != nil {
		t.Fatal("partial cancellation committed")
	}
	if _, err := f.pool.Exec(t.Context(), `ALTER TABLE events DROP CONSTRAINT reject_cancel`); err != nil {
		t.Fatal(err)
	}
	f.request(t, "POST", "/api/v1/runs/"+id+"/cancel", "", 200)
}

func TestSSEHeartbeatThenNewEventAndMissingRunBeforeStream(t *testing.T) {
	f := setup(t)
	id := f.run(t)["id"].(string)
	f.request(t, "GET", "/api/v1/runs/"+uuid.NewString()+"/events/stream", "", 404)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", f.server.URL+"/api/v1/runs/"+id+"/events/stream", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(response.Header)
	}
	r := bufio.NewReader(response.Body)
	line, err := r.ReadString('\n')
	if err != nil || line != ": keep-alive\n" {
		t.Fatal(line, err)
	}
	f.request(t, "POST", "/api/v1/runs/"+id+"/cancel", "", 200)
	for {
		line, err = r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "id: 1\n" {
			break
		}
	}
	line, err = r.ReadString('\n')
	if err != nil || line != "event: run.cancelled\n" {
		t.Fatal(line, err)
	}
}

func TestAPIConfigurationRejectsUnsafeIntervalsAndMalformedOrigins(t *testing.T) {
	for _, value := range []string{"NaN", "Inf", "-1", "0", "1e30", "1e-30"} {
		if _, err := httpapi.LoadConfig(func(key string) string {
			if key == "SSE_POLL_INTERVAL_SECONDS" {
				return value
			}
			return ""
		}); err == nil {
			t.Fatal("invalid poll interval accepted")
		}
	}
	if _, err := httpapi.LoadConfig(func(key string) string {
		if key == "CORS_ORIGINS" {
			return "not-json"
		}
		return ""
	}); err == nil {
		t.Fatal("malformed origins accepted")
	}
	config, err := httpapi.LoadConfig(func(string) string { return "" })
	if err != nil || !filepath.IsAbs(config.ArtifactRoot) || config.SSEPollInterval != 500*time.Millisecond {
		t.Fatal(config, err)
	}
}
