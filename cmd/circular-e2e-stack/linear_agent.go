package main

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ruohao1/circular/internal/testsupport"
	"github.com/ruohao1/circular/internal/worker"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type fixtureQueue struct {
	worker.Queue
	paused atomic.Bool
}

func (q *fixtureQueue) Acquire(ctx context.Context, owner string) (*worker.Claim, error) {
	if q.paused.Load() {
		return nil, nil
	}
	return q.Queue.Acquire(ctx, owner)
}
func linearAgentFixtureHandler(pool *pgxpool.Pool, provider *testsupport.ProviderFixture, prefix string, queue *fixtureQueue, delivery *fixtureGitDelivery, restart func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			session := r.URL.Query().Get("session")
			items := []testsupport.FixtureLinearActivity{}
			for _, v := range provider.LinearActivities() {
				if v.SessionID == session {
					items = append(items, v)
				}
			}
			var runs, comments, leases int
			_ = pool.QueryRow(r.Context(), `SELECT count(*) FROM external_requests WHERE session_id=$1 AND run_id IS NOT NULL`, session).Scan(&runs)
			_ = pool.QueryRow(r.Context(), `SELECT count(*) FROM linear_run_updates WHERE run_id IN(SELECT s.run_id FROM external_session_runs s JOIN external_requests q ON q.id=s.request_id WHERE q.session_id=$1)`, session).Scan(&comments)
			_ = pool.QueryRow(r.Context(), `SELECT count(*) FROM workspaces w JOIN external_session_runs s ON s.run_id=w.run_id JOIN external_requests q ON q.id=s.request_id WHERE q.session_id=$1 AND w.status<>'released'`, session).Scan(&leases)
			_ = json.NewEncoder(w).Encode(map[string]any{"activities": items, "links": provider.LinearSessionLinks(session), "runs": runs, "ordinary_updates": comments, "active_workspaces": leases, "reviews": provider.ReviewReceipts()})
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var in struct {
			Project   string `json:"project_id"`
			Session   string `json:"session_id"`
			Action    string `json:"action"`
			Prompt    string `json:"prompt"`
			Signal    string `json:"signal"`
			Issue     string `json:"issue_id"`
			Humanless bool   `json:"humanless"`
			NonIssue  bool   `json:"non_issue"`
			Paused    *bool  `json:"paused"`
			Restart   bool   `json:"restart"`
			EnableGit bool   `json:"enable_git"`
			Outage    *bool  `json:"outage"`
			Revoked   *bool  `json:"revoked"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&in) != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		var name string
		if pool.QueryRow(r.Context(), `SELECT name FROM projects WHERE id=$1`, in.Project).Scan(&name) != nil || !strings.HasPrefix(name, prefix) {
			http.Error(w, "not an owned fixture project", 400)
			return
		}
		if in.Paused != nil {
			queue.paused.Store(*in.Paused)
		}
		if in.Restart {
			if e := restart(); e != nil {
				http.Error(w, "fixture restart failed", 500)
				return
			}
		}
		if in.Outage != nil {
			provider.Unavailable.Store(*in.Outage)
		}
		if in.Revoked != nil {
			org := "20000000-0000-4000-8000-000000000004"
			if *in.Revoked {
				org = uuid.NewString()
			}
			provider.SetLinearOrganization(org)
		}
		if in.EnableGit {
			delivery.enabled.Store(true)
			provider.ClearReviewPR()
			provider.SetGitHubCreation(testsupport.FixtureGitHubCreation{Administration: "write", Contents: "write", PullRequests: "write", AccountType: "User", Account: "circular-fixture", Attach: true})
		}
		if in.Session == "" {
			_ = json.NewEncoder(w).Encode(map[string]bool{"saved": true})
			return
		}
		if _, e := uuid.Parse(in.Session); e != nil {
			http.Error(w, "invalid session", 400)
			return
		}
		if in.Action == "" {
			in.Action = "created"
		}
		event := testsupport.LinearAgentEvent(in.Session, in.Action, in.Prompt)
		s := event["agentSession"].(map[string]any)
		if in.Humanless {
			s["creator"] = nil
			s["creatorId"] = nil
		}
		if in.NonIssue {
			s["issue"] = nil
			s["issueId"] = nil
		} else if in.Issue != "" {
			s["issueId"] = in.Issue
			s["issue"].(map[string]any)["id"] = in.Issue
		}
		if in.Action == "prompted" {
			event["agentActivity"].(map[string]any)["signal"] = in.Signal
		}
		provider.SetLinearAgentSession(s)
		event["webhookTimestamp"] = time.Now().UnixMilli()
		_ = json.NewEncoder(w).Encode(event)
	}
}
