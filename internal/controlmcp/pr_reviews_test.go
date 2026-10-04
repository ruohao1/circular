package controlmcp_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/controlmcp"
)

func TestPRReviewToolsKeepKeysAndSeparateReadOnlyRefreshFromLaunch(t *testing.T) {
	source, review, project, reviewer, key := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var launches, retries, refreshes, reads atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/runs/" + source + "/pr-reviews":
			if r.Method == "POST" {
				var input object
				if json.NewDecoder(r.Body).Decode(&input) != nil || input["request_key"] != key || input["expected_input_fingerprint"] != strings.Repeat("a", 64) || input["mode"] != "normal" {
					t.Error("request identity changed", input)
				}
				launches.Add(1)
			} else {
				if r.URL.Query().Get("cursor") != "a+/=b" {
					t.Error("cursor not encoded")
				}
				reads.Add(1)
			}
		case "/api/v1/pr-reviews/" + review + "/refresh":
			if r.Method != "POST" {
				t.Error("wrong refresh verb")
			}
			refreshes.Add(1)
		case "/api/v1/pr-reviews/" + review + "/publication/retry":
			retries.Add(1)
		default:
			reads.Add(1)
		}
		fmt.Fprintf(w, `{"id":%q,"status":"pending"}`, review)
	}))
	defer api.Close()
	client := connect(t, controlmcp.Config{APIURL: api.URL})
	ro := connect(t, controlmcp.Config{APIURL: api.URL, ReadOnly: true})
	args := object{"run_id": source, "reviewer_id": reviewer, "request_key": key, "expected_input_fingerprint": strings.Repeat("a", 64), "mode": "normal"}
	for range 2 {
		call(t, client, "launch_pr_review", args)
	}
	call(t, client, "retry_pr_review_publication", object{"review_id": review})
	call(t, ro, "refresh_pr_review", object{"review_id": review})
	call(t, ro, "prepare_pr_review", object{"run_id": source, "reviewer_id": reviewer})
	call(t, ro, "get_pr_review", object{"review_id": review})
	call(t, ro, "get_pr_review_settings", object{"project_id": project})
	call(t, ro, "list_pr_reviews", object{"run_id": source, "cursor": "a+/=b"})
	rejects(t, ro, "launch_pr_review", args, "")
	rejects(t, ro, "retry_pr_review_publication", object{"review_id": review}, "")
	rejects(t, ro, "set_pr_review_settings", object{"project_id": project, "automatic": true}, "")
	for _, invalid := range []string{"../run", uuid.Nil.String(), "not-an-id"} {
		rejects(t, client, "get_pr_review", object{"review_id": invalid}, "")
		rejects(t, client, "prepare_pr_review", object{"run_id": source, "reviewer_id": invalid}, "")
	}
	for _, field := range []string{"request_key", "expected_input_fingerprint", "mode"} {
		changed := object{}
		for k, v := range args {
			changed[k] = v
		}
		changed[field] = "bad"
		rejects(t, client, "launch_pr_review", changed, "")
	}
	if launches.Load() != 2 || retries.Load() != 1 || refreshes.Load() != 1 || reads.Load() != 4 {
		t.Fatal("authority crossed API boundary", launches.Load(), retries.Load(), refreshes.Load(), reads.Load())
	}
}
