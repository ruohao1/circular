package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LinearRunUpdatesSettings struct {
	Enabled      bool   `json:"enabled"`
	Authorized   bool   `json:"authorized"`
	PendingCount int    `json:"pending_count"`
	FailedCount  int    `json:"failed_count"`
	LastError    string `json:"last_error"`
}

type LinearRunDelivery struct {
	RequestID       string          `json:"request_id,omitempty"`
	Publisher       json.RawMessage `json:"publisher,omitempty"`
	IssueURL        string          `json:"issue_url"`
	Status          string          `json:"status"`
	Error           string          `json:"error"`
	LastDeliveredAt *time.Time      `json:"last_delivered_at"`
}

func (s *Service) LinearRunUpdates(ctx context.Context, project string) (LinearRunUpdatesSettings, error) {
	var result LinearRunUpdatesSettings
	err := s.pool.QueryRow(ctx, `SELECT
 COALESCE((SELECT enabled FROM linear_run_update_settings WHERE project_id=$1),false),
 COALESCE((SELECT authorized FROM linear_connection_state WHERE project_id=$1),false),
 (SELECT count(*) FROM linear_run_updates WHERE project_id=$1 AND status='pending'),
 (SELECT count(*) FROM linear_run_updates WHERE project_id=$1 AND status IN ('failed','uncertain')),
 COALESCE((SELECT last_error FROM linear_run_updates WHERE project_id=$1 AND status IN ('pending','failed','uncertain') AND last_error<>'' ORDER BY created_at DESC LIMIT 1),'')
 FROM projects WHERE id=$1`, project).Scan(&result.Enabled, &result.Authorized, &result.PendingCount, &result.FailedCount, &result.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrAccess
	}
	return result, err
}

func (s *Service) SetLinearRunUpdates(ctx context.Context, project string, enabled bool) (LinearRunUpdatesSettings, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LinearRunUpdatesSettings{}, err
	}
	defer rollback(ctx, tx)
	// Same lock order as delivery and disconnect. Authorizations cannot change
	// between the grant check and enabling future events.
	var authorized bool
	err = tx.QueryRow(ctx, `SELECT authorized FROM linear_connection_state WHERE project_id=$1`, project).Scan(&authorized)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return LinearRunUpdatesSettings{}, err
	}
	if enabled && !authorized {
		return LinearRunUpdatesSettings{}, ErrReconnect
	}
	if _, err = tx.Exec(ctx, `INSERT INTO linear_run_update_settings(project_id,enabled) VALUES($1,$2) ON CONFLICT(project_id) DO UPDATE SET enabled=EXCLUDED.enabled,updated_at=now()`, project, enabled); err != nil {
		return LinearRunUpdatesSettings{}, err
	}
	if !enabled {
		if _, err = tx.Exec(ctx, `UPDATE linear_run_updates SET status='skipped',last_error='Run updates were turned off before delivery' WHERE project_id=$1 AND status='pending' AND NOT started AND NOT legacy_reconcile`, project); err != nil {
			return LinearRunUpdatesSettings{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return LinearRunUpdatesSettings{}, err
	}
	return s.LinearRunUpdates(ctx, project)
}

func (s *Service) RunLinearDelivery(ctx context.Context, run string) (LinearRunDelivery, error) {
	result := LinearRunDelivery{Status: "not_linked"}
	var request string
	e := s.pool.QueryRow(ctx, `SELECT request_id FROM external_session_runs WHERE run_id=$1`, run).Scan(&request)
	if e == nil {
		detail, e := s.ExternalRequest(ctx, request)
		if e != nil {
			return result, e
		}
		result.RequestID = request
		result.IssueURL = detail.SourceURL
		result.Status = detail.DeliveryStatus
		result.Error = detail.DeliveryReason
		if result.Status == "" || result.Status == "cancelled" {
			result.Status = "pending"
		}
		_ = s.pool.QueryRow(ctx, `SELECT max(updated_at) FROM linear_agent_activities WHERE request_id=$1 AND status='delivered'`, request).Scan(&result.LastDeliveredAt)
		return result, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return result, e
	}

	var project, runStatus string
	var rawURL *string
	err := s.pool.QueryRow(ctx, `SELECT t.project_id,t.external_refs->'linear'->>'url',r.status FROM runs r JOIN tasks t ON t.id=r.task_id WHERE r.id=$1`, run).Scan(&project, &rawURL, &runStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrAccess
	}
	if err != nil {
		return result, err
	}
	if rawURL == nil || !validLinearURL(*rawURL) {
		return result, nil
	}
	result.IssueURL = *rawURL
	var attempts int
	err = s.pool.QueryRow(ctx, `SELECT status,last_error,attempts,(SELECT max(delivered_at) FROM linear_run_updates WHERE run_id=$1),publisher FROM linear_run_updates WHERE run_id=$1 ORDER BY CASE phase WHEN 'pull_request' THEN 2 WHEN 'terminal' THEN 1 ELSE 0 END DESC LIMIT 1`, run).Scan(&result.Status, &result.Error, &attempts, &result.LastDeliveredAt, &result.Publisher)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	settings, err := s.LinearRunUpdates(ctx, project)
	if err != nil {
		return result, err
	}
	switch {
	case result.Status == "delivered" || result.Status == "skipped" || result.Status == "failed" || result.Status == "uncertain":
	case result.Status == "not_linked" && (runStatus == "succeeded" || runStatus == "failed" || runStatus == "cancelled"):
		result.Status = "skipped"
		result.Error = "Publishing was not enabled for this run transition"
	case !settings.Enabled:
		result.Status = "disabled"
	case !settings.Authorized:
		result.Status = "reconnect_required"
	case result.Status == "not_linked":
		result.Status = "pending"
		result.Error = "Waiting for the next run transition"
	case attempts > 0:
		result.Status = "retrying"
	}
	return result, nil
}

func validLinearURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host == "linear.app" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasPrefix(u.Path, "/") && len(u.Path) > 1
}

// RunLinearUpdates processes durable work until cancellation. Multiple API
// processes are safe: each delivery serializes its connection and locks its row.
// Provider outages remain in the outbox; only storage failures stop the loop.
func (s *Service) RunLinearUpdates(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		worked, err := s.ProcessLinearUpdate(ctx)
		if err != nil {
			return err
		}
		if worked {
			continue
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type linearUpdate struct {
	ID, Project, Run, Phase, Outcome, Account, Issue, IssueURL, Summary, Body string
	Attempts                                                                  int
	Owner                                                                     string
	Publisher                                                                 *PublisherIdentity
	Started, Legacy                                                           bool
}

// ProcessLinearUpdate attempts one due update. It is also the deterministic
// integration seam for owned provider tests; it never starts a coding run.
func (s *Service) ProcessLinearUpdate(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	u := linearUpdate{Owner: uuid.NewString()}
	var raw []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS (
 SELECT u.id FROM linear_run_updates u WHERE u.status='pending' AND (u.lease_until IS NULL OR u.lease_until<now())
 AND (u.next_attempt_at<=now() OR (u.phase='running' AND NOT u.started AND EXISTS(SELECT 1 FROM linear_run_updates terminal WHERE terminal.run_id=u.run_id AND terminal.phase='terminal')))
 AND NOT EXISTS(SELECT 1 FROM linear_run_updates prior WHERE prior.run_id=u.run_id AND ((prior.phase='running' AND u.phase IN ('terminal','pull_request')) OR (prior.phase='terminal' AND u.phase='pull_request')) AND prior.status='pending')
 ORDER BY u.next_attempt_at,u.created_at FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE linear_run_updates u SET lease_owner=$1,lease_until=now()+interval '2 minutes' FROM candidate c WHERE c.id=u.id
 RETURNING u.id,u.project_id,u.run_id,u.phase,u.outcome,u.account_id,u.issue_id,u.issue_url,u.summary,u.body,u.attempts,u.publisher,u.started,u.legacy_reconcile`, u.Owner).Scan(&u.ID, &u.Project, &u.Run, &u.Phase, &u.Outcome, &u.Account, &u.Issue, &u.IssueURL, &u.Summary, &u.Body, &u.Attempts, &raw, &u.Started, &u.Legacy)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func(status, message string, delay time.Duration) (bool, error) {
		record, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		publisher, _ := json.Marshal(u.Publisher)
		command, err := s.pool.Exec(record, `UPDATE linear_run_updates SET status=$3,last_error=$4,attempts=LEAST(attempts+1,1000000),next_attempt_at=now()+$5::interval,body=$6,publisher=COALESCE(publisher,NULLIF($7::jsonb,'null'::jsonb)),delivered_at=CASE WHEN $3='delivered' THEN now() ELSE delivered_at END,lease_owner=NULL,lease_until=NULL WHERE id=$1 AND lease_owner=$2 AND lease_until>now()`, u.ID, u.Owner, status, message, fmt.Sprintf("%f seconds", delay.Seconds()), u.Body, publisher)
		if err != nil {
			return false, err
		}
		return command.RowsAffected() == 1, nil
	}
	if len(raw) > 0 && (json.Unmarshal(raw, &u.Publisher) != nil || u.Publisher == nil || !u.Publisher.valid("linear")) {
		return finish("failed", "The saved publication identity is invalid", 0)
	}
	freshBody := u.Body == ""
	if freshBody {
		u.Body = linearUpdateBody(s.config.WebURL, u)
		if !u.Legacy {
			var name string
			if err := s.pool.QueryRow(ctx, `SELECT a.name FROM runs r JOIN agents a ON a.id=r.agent_id WHERE r.id=$1`, u.Run).Scan(&name); err != nil {
				return false, err
			}
			u.Body += "\n\nAgent:\n\n" + cleanLinearSummary(name)
		}
	}
	// Started and pre-upgrade writes are read-only, even after settings change.
	if u.Started || u.Legacy {
		reader := u.Publisher
		if reader == nil {
			identity, err := s.PublicationIdentity(ctx, u.Project, "linear", u.Account)
			if err != nil {
				return finish("pending", err.Error(), retryDelay(u.Attempts))
			}
			reader = &identity
		}
		var receipt *linearCommentReceipt
		err = s.withLinearPublisher(ctx, u.Project, *reader, true, func(token string) error {
			var err error
			receipt, err = s.linearCommentReceipt(ctx, token, u)
			return err
		})
		if err == nil && receipt != nil {
			if u.Publisher == nil {
				u.Publisher = &PublisherIdentity{Mode: "user", Provider: "linear", AccountID: u.Account, ActorID: receipt.User.ID}
			}
			return finish("delivered", "", 0)
		}
		if err != nil && !errors.Is(err, ErrAccess) {
			return finish("pending", ErrProvider.Error(), retryDelay(u.Attempts))
		}
		return finish("uncertain", errPublicationUncertain.Error(), 0)
	}
	var enabled, connected bool
	var accountID string
	err = s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT enabled FROM linear_run_update_settings WHERE project_id=$1),false),COALESCE((SELECT enabled FROM linear_connection_state WHERE project_id=$1),false),COALESCE((SELECT account_id FROM linear_connection_state WHERE project_id=$1),'')`, u.Project).Scan(&enabled, &connected, &accountID)
	if err != nil {
		return false, err
	}
	if !enabled || !connected {
		return finish("skipped", "Run updates were turned off or Linear was disconnected before delivery", 0)
	}
	if accountID != u.Account {
		return finish("skipped", "The connected Linear workspace changed before delivery", 0)
	}
	if u.Phase == "running" {
		var terminal bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM linear_run_updates WHERE run_id=$1 AND phase='terminal')`, u.Run).Scan(&terminal); err != nil {
			return false, err
		}
		if terminal {
			return finish("skipped", "The run finished before its progress update was delivered", 0)
		}
	}
	if !validLinearURL(u.IssueURL) {
		return finish("failed", "The linked Linear issue URL is invalid", 0)
	}
	if u.Publisher == nil {
		identity, err := s.PublicationIdentity(ctx, u.Project, "linear", u.Account)
		if err != nil {
			return finish("pending", err.Error(), retryDelay(u.Attempts))
		}
		u.Publisher = &identity
	}
	// Pin author and body before provider calls. This is independent of the write
	// reservation, so a definitively rejected POST never changes its next author.
	publisher, _ := json.Marshal(u.Publisher)
	command, err := s.pool.Exec(ctx, `UPDATE linear_run_updates SET publisher=COALESCE(publisher,$3::jsonb),body=$4 WHERE id=$1 AND lease_owner=$2 AND lease_until>now() AND NOT started`, u.ID, u.Owner, publisher, u.Body)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() != 1 {
		return false, nil
	}
	if freshBody && u.Phase == "pr_review" {
		return finish("pending", "", 0)
	}
	err = s.withLinearPublisher(ctx, u.Project, *u.Publisher, false, func(token string) error {
		receipt, err := s.linearCommentReceipt(ctx, token, u)
		if err != nil {
			return err
		}
		if receipt != nil {
			return nil
		}
		issue, err := s.linearIssue(ctx, token, u.Issue)
		if err != nil {
			return err
		}
		if !validLinearURL(issue.URL) {
			return ErrAccess
		}
		command, err := s.pool.Exec(ctx, `UPDATE linear_run_updates SET started=true WHERE id=$1 AND lease_owner=$2 AND lease_until>now() AND NOT started AND status='pending' AND EXISTS(SELECT 1 FROM linear_run_update_settings settings JOIN linear_connection_state c ON c.project_id=settings.project_id WHERE settings.project_id=$3 AND settings.enabled AND c.authorized)`, u.ID, u.Owner, u.Project)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return ErrIdentityUnavailable
		}
		u.Started = true
		err = s.createLinearComment(ctx, token, u)
		if errors.Is(err, ErrAccess) || errors.Is(err, ErrReconnect) {
			if _, saveErr := s.pool.Exec(ctx, `UPDATE linear_run_updates SET started=false WHERE id=$1 AND lease_owner=$2 AND lease_until>now()`, u.ID, u.Owner); saveErr != nil {
				return saveErr
			}
			u.Started = false
		}
		return err
	})
	if err == nil {
		return finish("delivered", "", 0)
	}
	if errors.Is(err, ErrAccess) {
		return finish("failed", "Linear denied access to the linked issue or permission to comment", 0)
	}
	if errors.Is(err, ErrIdentityUnavailable) || errors.Is(err, ErrReconnect) {
		return finish("pending", ErrReconnect.Error(), 5*time.Minute)
	}
	return finish("pending", ErrProvider.Error(), retryDelay(u.Attempts))
}

func retryDelay(attempt int) time.Duration {
	return min(time.Duration(1<<min(attempt, 7))*30*time.Second, time.Hour)
}

type linearCommentReceipt struct {
	ID    string `json:"id"`
	Body  string `json:"body"`
	Issue *struct {
		ID string `json:"id"`
	} `json:"issue"`
	User *struct {
		ID string `json:"id"`
	} `json:"user"`
}

func (s *Service) linearCommentReceipt(ctx context.Context, token string, u linearUpdate) (*linearCommentReceipt, error) {
	var response struct {
		Comments struct {
			Nodes []linearCommentReceipt `json:"nodes"`
		} `json:"comments"`
	}
	if err := s.linear(ctx, token, `query CircularDeliveredComment($id: ID!) { comments(first: 2, filter: {id: {eq: $id}}) { nodes { id body user { id } issue { id } } } }`, map[string]any{"id": u.ID}, &response); err != nil {
		return nil, err
	}
	if len(response.Comments.Nodes) == 0 {
		return nil, nil
	}
	if len(response.Comments.Nodes) != 1 {
		return nil, ErrAccess
	}
	receipt := response.Comments.Nodes[0]
	if !matchesLinearComment(receipt, u) {
		return nil, ErrAccess
	}
	return &receipt, nil
}
func matchesLinearComment(receipt linearCommentReceipt, u linearUpdate) bool {
	if receipt.ID != u.ID || receipt.Body != u.Body || receipt.Issue == nil || receipt.Issue.ID != u.Issue || receipt.User == nil {
		return false
	}
	if _, err := uuid.Parse(receipt.User.ID); err != nil {
		return false
	}
	return u.Publisher == nil || receipt.User.ID == u.Publisher.ActorID
}
func (s *Service) createLinearComment(ctx context.Context, token string, u linearUpdate) error {
	var response struct {
		CommentCreate struct {
			Success bool                  `json:"success"`
			Comment *linearCommentReceipt `json:"comment"`
		} `json:"commentCreate"`
	}
	if err := s.linear(ctx, token, `mutation CircularRunComment($input: CommentCreateInput!) { commentCreate(input: $input) { success comment { id body user { id } issue { id } } } }`, map[string]any{"input": map[string]any{"id": u.ID, "issueId": u.Issue, "body": u.Body}}, &response); err != nil {
		return err
	}
	if !response.CommentCreate.Success || response.CommentCreate.Comment == nil || !matchesLinearComment(*response.CommentCreate.Comment, u) {
		return ErrProvider
	}
	return nil
}

var githubPRPattern = regexp.MustCompile(`https://github\.com/[^\s<>"']+`)
var githubPRPath = regexp.MustCompile(`^/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/[1-9][0-9]*$`)
var sensitiveText = regexp.MustCompile(`(?i)(?:\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{8,}|github_pat_[A-Za-z0-9_]{8,}|lin_(?:api|oauth)_[A-Za-z0-9_-]{8,})|(?:api[_ -]?key|access[_ -]?token|refresh[_ -]?token|authorization|password|secret)\s*[:=]\s*\S+)`)

func linearUpdateBody(webURL string, u linearUpdate) string {
	if u.Phase == "pr_review" {
		return linearReviewBody(webURL, u)
	}
	if u.Phase == "pull_request" {
		return "**Circular draft pull request ready**\n\n[Review pull request](" + u.Summary + ")\n\n[Open run in Circular](" + webURL + "/runs/" + u.Run + ")"
	}
	outcome := map[string]string{"running": "started", "succeeded": "completed", "failed": "failed", "cancelled": "was cancelled"}[u.Outcome]
	body := "**Circular run " + outcome + "**\n\n[Open run in Circular](" + webURL + "/runs/" + u.Run + ")"
	if u.Phase == "running" {
		return body
	}
	summary := cleanLinearSummary(u.Summary)
	if summary != "" {
		body += "\n\nAgent summary:\n\n" + summary
	}
	links := []string{}
	for _, match := range githubPRPattern.FindAllString(u.Summary, 10) {
		value := strings.TrimRight(match, ".,;:!)]}> \n\r\t\"'")
		parsed, err := url.Parse(value)
		if err == nil && parsed.Scheme == "https" && parsed.Host == "github.com" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && githubPRPath.MatchString(parsed.Path) && !slices.Contains(links, value) {
			links = append(links, value)
		}
	}
	if len(links) > 0 {
		body += "\n\nPull requests reported by the agent:"
		for _, link := range links {
			body += "\n- " + link
		}
	}
	return body
}

// Publish a bounded text excerpt, excluding fenced code/diffs and common secret
// forms. Plain quoting prevents agent output from creating mentions or images.
func cleanLinearSummary(value string) string {
	value = sensitiveText.ReplaceAllString(value, "[redacted]")
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	lines := []string{}
	fenced := false
	diff := false
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fenced = !fenced
			continue
		}
		if strings.HasPrefix(trimmed, "diff --git") || strings.HasPrefix(trimmed, "@@") || strings.HasPrefix(trimmed, "+++ ") || strings.HasPrefix(trimmed, "--- ") {
			diff = true
		}
		if diff && trimmed == "" {
			diff = false
		}
		if fenced || diff {
			continue
		}
		line = strings.NewReplacer("@", "@\u200b", "\\", "\\\\", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;", "!", "\\!", "`", "\\`").Replace(line)
		lines = append(lines, line)
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	runes := []rune(text)
	if len(runes) > 2400 {
		text = string(runes[:2400]) + "…"
	}
	if text == "" {
		return ""
	}
	return "> " + strings.ReplaceAll(text, "\n", "\n> ")
}
