package integrations_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func queueReviewSummary(t *testing.T, f fixture, r prreviews.Review) {
	t.Helper()
	tx, err := f.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = postgres.QueuePRReviewLinear(t.Context(), tx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestReviewLinearSummaryIsIndependentAndFreezesBeforeDelivery(t *testing.T) {
	for _, mode := range []string{"delayed_github", "early_github", "missing_report", "off", "workspace_changed", "disconnect", "grant_revoked", "lost_response"} {
		t.Run(mode, func(t *testing.T) {
			f, r := completedReview(t)
			f.connect(t, "linear")
			if mode != "off" {
				enable(t, f)
			}
			if mode == "missing_report" {
				if _, err := f.pool.Exec(t.Context(), `UPDATE pr_reviews SET candidate_report=NULL,assessment='incomplete',report_error='No structured report' WHERE id=$1`, r.ID); err != nil {
					t.Fatal(err)
				}
			}
			queueReviewSummary(t, f, r)
			queueReviewSummary(t, f, r)
			if mode == "off" {
				var count int
				if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM linear_run_updates WHERE run_id=$1`, r.RunID).Scan(&count); err != nil || count != 0 {
					t.Fatal(count, err)
				}
				return
			}
			if mode == "workspace_changed" {
				f.provider.SetLinearOrganization(uuid.NewString())
				f.connect(t, "linear")
			}
			if mode == "disconnect" {
				if _, err := f.service.Disconnect(t.Context(), f.project, "linear"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "grant_revoked" {
				if _, err := f.pool.Exec(t.Context(), `UPDATE integrations SET granted_scopes=ARRAY['read'] WHERE project_id=$1 AND provider='linear'`, f.project); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "early_github" {
				if _, err := f.service.ProcessPRReviewPublication(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			// First pass freezes the body. Later GitHub delivery must not rewrite it.
			if _, err := f.service.ProcessLinearUpdate(t.Context()); err != nil {
				t.Fatal(err)
			}
			if mode == "delayed_github" {
				if _, err := f.service.ProcessPRReviewPublication(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "lost_response" {
				f.provider.LoseCommentResponse.Store(true)
			}
			for range 3 {
				if _, err := f.service.ProcessLinearUpdate(t.Context()); err != nil {
					t.Fatal(err)
				}
				due(t, f)
			}
			comments := f.provider.LinearComments()
			if mode == "workspace_changed" || mode == "disconnect" || mode == "grant_revoked" {
				if len(comments) != 0 {
					t.Fatal("guard bypassed", mode)
				}
				if mode == "grant_revoked" {
					f.connect(t, "linear")
					due(t, f)
					for range 2 {
						if _, err := f.service.ProcessLinearUpdate(t.Context()); err != nil {
							t.Fatal(err)
						}
					}
					if len(f.provider.LinearComments()) != 1 {
						t.Fatal("reconnect did not resume")
					}
				}
				return
			}
			if len(comments) != 1 || f.provider.CommentCreates.Load() != 1 {
				t.Fatal("summary missing or duplicated", len(comments))
			}
			body := comments[0].Body
			if !strings.Contains(body, "Circular PR review") || !strings.Contains(body, r.RunID.String()) || !strings.Contains(body, r.Snapshot.PR.HeadSHA) || strings.Contains(body, "Circular run") {
				t.Fatal(body)
			}
			if strings.Contains(body, "View GitHub feedback") != (mode == "early_github") {
				t.Fatal("started body changed or early link missing", body)
			}
			if mode == "missing_report" && (!strings.Contains(body, "incomplete") || strings.Contains(body, "0 blocking")) {
				t.Fatal("missing report claimed clean", body)
			}
			var stored prreviews.LinearSummary
			var raw string
			if err := f.pool.QueryRow(t.Context(), `SELECT summary FROM linear_run_updates WHERE run_id=$1 AND phase='pr_review'`, r.RunID).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &stored); err != nil || stored.ReviewID == uuid.Nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReviewNativeSessionDeliveryStatus(t *testing.T) {
	f, r := completedReview(t)
	identity, request := uuid.NewString(), uuid.NewString()
	_, e := f.pool.Exec(t.Context(), `WITH i AS(INSERT INTO provider_identities(id,provider,app_client_id,account_id,account_name,actor_id,actor_name,status) VALUES($1,'linear','fixture-linear','workspace','Fixture','actor','Circular','available')),q AS(INSERT INTO external_requests(id,identity_id,session_id,input_fingerprint,status) VALUES($2,$1,$3,'fixture','succeeded')) INSERT INTO external_session_runs(run_id,request_id) VALUES($4,$2)`, identity, request, uuid.NewString(), r.RunID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE linear_agent_activities SET status='delivered' WHERE request_id=$1`, request); e != nil {
		t.Fatal(e)
	}
	got, e := f.service.PRReview(t.Context(), r.ID.String())
	if e != nil || got.Linear.Status != "delivered" {
		t.Fatal("native delivery reported skipped", got.Linear, e)
	}
}
