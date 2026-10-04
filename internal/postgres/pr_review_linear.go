package postgres

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/prreviews"
)

// QueuePRReviewLinear shares the successful completion transaction. The frozen
// issue binding and current opt-in/grant must agree; delivery rechecks access.
func QueuePRReviewLinear(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	var linked bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM external_session_runs s JOIN pr_reviews p ON p.run_id=s.run_id WHERE p.id=$1)`, id).Scan(&linked); err != nil {
		return err
	}
	if linked {
		return nil
	}
	var snapshot, report []byte
	var summary prreviews.LinearSummary
	summary.ReviewID = id
	err := tx.QueryRow(ctx, `SELECT p.run_id,p.snapshot,p.candidate_report,p.assessment,p.report_error FROM pr_reviews p JOIN runs r ON r.id=p.run_id WHERE p.id=$1 AND r.kind='pr_review' AND r.status='succeeded'`, id).Scan(&summary.RunID, &snapshot, &report, &summary.Assessment, &summary.ReportError)
	if err != nil {
		return err
	}
	var value prreviews.LaunchSnapshot
	if err = json.Unmarshal(snapshot, &value); err != nil {
		return err
	}
	summary.HeadSHA = value.PR.HeadSHA
	if len(report) > 0 {
		var r prreviews.Report
		if err = json.Unmarshal(report, &r); err != nil {
			return err
		}
		for _, f := range r.Findings {
			switch f.Severity {
			case "critical":
				summary.Critical++
			case "high":
				summary.High++
			case "medium":
				summary.Medium++
			case "low":
				summary.Low++
			}
		}
	}
	var ext struct {
		Linear struct {
			Issue   string `json:"issue_id"`
			Account string `json:"account_id"`
			URL     string `json:"url"`
		} `json:"linear"`
	}
	if json.Unmarshal(value.TaskExternalRefs, &ext) != nil {
		return nil
	}
	if _, err = uuid.Parse(ext.Linear.Issue); err != nil {
		return nil
	}
	link, err := url.Parse(ext.Linear.URL)
	if err != nil || link.Scheme != "https" || link.Host != "linear.app" || link.User != nil || link.RawQuery != "" || link.Fragment != "" || !strings.HasPrefix(link.Path, "/") {
		return nil
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO linear_run_updates(project_id,run_id,phase,outcome,account_id,issue_id,issue_url,summary)
 SELECT $1,$2,'pr_review','succeeded',i.account_id,$3::uuid,$4,$5 FROM linear_connection_state i JOIN linear_run_update_settings settings ON settings.project_id=i.project_id AND settings.enabled WHERE i.project_id=$1 AND i.authorized AND i.account_id<>'' AND (($6<>'' AND i.account_id=$6) OR ($6='' AND left($4,length(i.account_url)+1)=(i.account_url)||'/')) ON CONFLICT(run_id,phase) DO NOTHING`, value.ProjectID, summary.RunID, ext.Linear.Issue, ext.Linear.URL, string(raw), ext.Linear.Account)
	return err
}
