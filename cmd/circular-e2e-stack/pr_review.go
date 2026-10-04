package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/testsupport"
)

// prepareReviewFixture creates only owned source and a transport wrapper in the
// disposable root. Production GitHub identity/authentication checks still run.
func prepareReviewFixture(ctx context.Context, root string) (source, binary, base, head string, err error) {
	source = filepath.Join(root, "review-source")
	if err = os.MkdirAll(filepath.Join(source, "src"), 0700); err != nil {
		return
	}
	var realGit string
	realGit, err = exec.LookPath("git")
	if err != nil {
		return
	}
	git := func(args ...string) string {
		if err != nil {
			return ""
		}
		var output []byte
		output, err = exec.CommandContext(ctx, realGit, append([]string{"-C", source}, args...)...).CombinedOutput()
		if err != nil {
			err = fmt.Errorf("fixture git failed: %s", output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--initial-branch=main")
	git("config", "user.name", "Browser fixture")
	git("config", "user.email", "fixture@example.test")
	if err != nil {
		return
	}
	err = os.WriteFile(filepath.Join(source, "src/validate.ts"), []byte("export function valid(value: number) {\n  return true;\n}\n"), 0644)
	if err != nil {
		return
	}
	git("add", ".")
	git("commit", "-m", "Base")
	base = git("rev-parse", "HEAD")
	if err != nil {
		return
	}
	git("checkout", "-b", "circular/fixture-review")
	err = os.WriteFile(filepath.Join(source, "src/validate.ts"), []byte("export function valid(value: number) {\n  return value > 0;\n}\n"), 0644)
	if err != nil {
		return
	}
	git("add", ".")
	git("commit", "-m", "Reject negative inputs")
	head = git("rev-parse", "HEAD")
	git("checkout", "main")
	if err != nil {
		return
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	binary = filepath.Join(root, "review-fixture-git")
	script := "#!/bin/sh\ncase \" $* \" in *\" clone \"*|*\" fetch \"*) exec " + quote(realGit) + " -c " + quote("url."+source+".insteadOf=https://github.com/fixture/private-source.git") + " \"$@\" ;; *) exec " + quote(realGit) + " \"$@\" ;; esac\n"
	err = os.WriteFile(binary, []byte(script), 0700)
	return
}

func reviewFixtureHandler(pool *pgxpool.Pool, provider *testsupport.ProviderFixture, prefix, source, base, head string) http.HandlerFunc {
	var mu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"reviews": provider.ReviewReceipts()})
		case http.MethodDelete:
			provider.ClearReviewPR()
			_ = json.NewEncoder(w).Encode(map[string]bool{"cleared": true})
		case http.MethodPost:
			var input struct {
				ProjectID uuid.UUID `json:"project_id"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
				http.Error(w, "invalid fixture request", 400)
				return
			}
			var name string
			if pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id=$1`, input.ProjectID).Scan(&name) != nil || !strings.HasPrefix(name, prefix) {
				http.Error(w, "not an owned fixture project", 400)
				return
			}
			snapshot, err := testsupport.SeedPRReviewSourceContext(r.Context(), pool, input.ProjectID)
			if err == nil {
				_, err = exec.CommandContext(r.Context(), "git", "-C", source, "branch", snapshot.PR.HeadRef, head).CombinedOutput()
			}
			if err == nil {
				_, err = pool.Exec(r.Context(), `UPDATE github_run_deliveries SET base_commit=$2,commit_sha=$3 WHERE run_id=$1`, snapshot.SourceRunID, base, head)
			}
			if err == nil {
				_, err = pool.Exec(r.Context(), `UPDATE tasks SET external_refs=jsonb_set(external_refs::jsonb,'{linear,account_id}','"20000000-0000-4000-8000-000000000004"') WHERE id=$1`, snapshot.TaskID)
			}
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			snapshot.PR.BaseSHA, snapshot.PR.HeadSHA = base, head
			provider.SetReviewPR(snapshot.PR, "open")
			_ = json.NewEncoder(w).Encode(map[string]any{"source_run_id": snapshot.SourceRunID, "task_id": snapshot.TaskID, "head_sha": head})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
