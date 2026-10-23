package testsupport

import (
	"github.com/ruohao1/circular/internal/prreviews"
	"net/http"
	"strings"
)

func serveCompletionFixture(w http.ResponseWriter, r *http.Request, pr prreviews.PRIdentity) bool {
	prefix := "/repos/" + pr.RepositoryName
	tested, tree := strings.Repeat("c", 40), strings.Repeat("d", 40)
	switch {
	case r.URL.Path == prefix+"/actions/workflows":
		fixtureJSON(w, map[string]any{"total_count": 1, "workflows": []any{map[string]any{"id": 123, "path": ".github/workflows/ci.yml", "state": "active"}}})
	case r.URL.Path == prefix+"/actions/workflows/123/runs":
		runs := []any{}
		if sha := r.URL.Query().Get("head_sha"); sha == tested || sha == "" {
			runs = append(runs, map[string]any{"id": 10, "workflow_id": 123, "run_number": 1, "run_attempt": 1, "head_sha": tested, "check_suite_id": 40, "status": "completed", "conclusion": "success", "path": ".github/workflows/ci.yml"})
		}
		fixtureJSON(w, map[string]any{"total_count": len(runs), "workflow_runs": runs})
	case r.URL.Path == prefix+"/actions/runs/10/attempts/1/jobs":
		fixtureJSON(w, map[string]any{"total_count": 1, "jobs": []any{map[string]any{"id": 20, "run_id": 10, "head_sha": tested, "name": "Tests", "status": "completed", "conclusion": "success", "check_run_url": "http://" + r.Host + prefix + "/check-runs/30"}}})
	case r.URL.Path == prefix+"/check-suites/40/check-runs":
		fixtureJSON(w, map[string]any{"total_count": 1, "check_runs": []any{map[string]any{"id": 30, "head_sha": tested, "name": "Tests", "status": "completed", "conclusion": "success", "app": map[string]any{"id": 42, "slug": "github-actions"}, "check_suite": map[string]any{"id": 40}}}})
	case strings.HasPrefix(r.URL.Path, prefix+"/git/commits/"):
		sha := strings.TrimPrefix(r.URL.Path, prefix+"/git/commits/")
		fixtureJSON(w, map[string]any{"sha": sha, "tree": map[string]any{"sha": tree}, "parents": []any{map[string]any{"sha": pr.BaseSHA}, map[string]any{"sha": pr.HeadSHA}}})
	case strings.HasPrefix(r.URL.Path, prefix+"/compare/"):
		fixtureJSON(w, map[string]any{"status": "ahead", "behind_by": 0, "base_commit": map[string]any{"sha": pr.BaseSHA}, "merge_base_commit": map[string]any{"sha": pr.BaseSHA}})
	default:
		return false
	}
	return true
}
