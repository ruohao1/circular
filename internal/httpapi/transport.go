package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func LoadConfig(getenv func(string) string) (Config, error) {
	root := getenv("CIRCULAR_ARTIFACT_ROOT")
	if root == "" {
		root = ".circular/artifacts"
	}
	if root == "~" || strings.HasPrefix(root, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, fmt.Errorf("cannot resolve artifact directory")
		}
		if root == "~" {
			root = home
		} else {
			root = filepath.Join(home, root[2:])
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return Config{}, fmt.Errorf("invalid artifact directory")
	}
	config := Config{ArtifactRoot: root, CORSOrigins: []string{"http://localhost:5173"}, SSEPollInterval: 500 * time.Millisecond}
	if origins := getenv("CORS_ORIGINS"); origins != "" {
		if err := json.Unmarshal([]byte(origins), &config.CORSOrigins); err != nil {
			return Config{}, fmt.Errorf("CORS_ORIGINS must be a JSON array of strings")
		}
	}
	if value := getenv("SSE_POLL_INTERVAL_SECONDS"); value != "" {
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return Config{}, fmt.Errorf("SSE_POLL_INTERVAL_SECONDS must be finite and positive")
		}
		config.SSEPollInterval = time.Duration(seconds * float64(time.Second))
		if config.SSEPollInterval <= 0 {
			return Config{}, fmt.Errorf("SSE poll interval is too small")
		}
	}
	return config, nil
}

func (a *api) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := false
		wildcard := false
		for _, value := range a.config.CORSOrigins {
			if value == "*" {
				allowed = true
				wildcard = true
			}
			if origin == value {
				allowed = true
			}
		}
		if origin != "" && allowed {
			if wildcard {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
		}
		if r.Method == "OPTIONS" && r.Header.Get("Access-Control-Request-Method") != "" {
			if !allowed {
				http.Error(w, "Disallowed CORS origin", 400)
				return
			}
			switch r.Header.Get("Access-Control-Request-Method") {
			case "DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT":
			default:
				http.Error(w, "Disallowed CORS method", 400)
				return
			}
			w.Header().Set("Access-Control-Allow-Methods", "DELETE, GET, HEAD, OPTIONS, PATCH, POST, PUT")
			w.Header().Set("Access-Control-Allow-Headers", r.Header.Get("Access-Control-Request-Headers"))
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(200)
			_, _ = w.Write([]byte("OK"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>Circular API</title><link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css"></head><body><div id="swagger-ui"></div><script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script><script>SwaggerUIBundle({url:'/openapi.json',dom_id:'#swagger-ui',deepLinking:true});</script></body></html>`))
}

func (a *api) stream(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	var last int64
	if values, exists := r.Header["Last-Event-Id"]; exists {
		value, err := strconv.ParseInt(strings.TrimSpace(values[0]), 10, 64)
		if err != nil || value < 0 {
			problem(w, 400, "Last-Event-ID must be a non-negative integer")
			return
		}
		last = value
	}
	after, ok := integerQuery(w, r, "after", 0, 0, 0)
	if !ok {
		return
	}
	if after > last {
		last = after
	}
	if !a.exists(w, r, id) {
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	for r.Context().Err() == nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		events, err := records(ctx, a.pool, "events", "EventRead", "WHERE t.run_id=$1 AND t.sequence>$2 ORDER BY t.sequence LIMIT 200", id, last)
		cancel()
		if err != nil {
			return
		}
		if len(events) == 0 {
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
			timer := time.NewTimer(a.config.SSEPollInterval)
			select {
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		for _, raw := range events {
			var event struct {
				Sequence int64  `json:"sequence"`
				Type     string `json:"type"`
			}
			if json.Unmarshal(raw, &event) != nil || strings.ContainsAny(event.Type, "\r\n") {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, raw); err != nil {
				return
			}
			if controller.Flush() != nil {
				return
			}
			last = event.Sequence
		}
	}
}

func (a *api) artifact(w http.ResponseWriter, r *http.Request) {
	id, ok := identifier(w, r.PathValue("run_id"), "path", "run_id")
	if !ok {
		return
	}
	artifactID, ok := identifier(w, r.PathValue("artifact_id"), "path", "artifact_id")
	if !ok {
		return
	}
	var kind, uri string
	var metadata map[string]any
	err := a.pool.QueryRow(r.Context(), "SELECT kind,uri,metadata FROM artifacts WHERE id=$1 AND run_id=$2", artifactID, id).Scan(&kind, &uri, &metadata)
	if dbError(w, err, "artifact") {
		return
	}
	data, err := a.content.Read(r.Context(), id, uri)
	if err != nil {
		problem(w, 404, "artifact content unavailable")
		return
	}
	if expected, ok := metadata["sha256"].(string); ok && expected != "" {
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != expected {
			problem(w, 409, "artifact integrity check failed")
			return
		}
	}
	extension := "tar"
	if kind == "diff" {
		extension = "patch"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, artifactID, extension))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
