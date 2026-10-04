package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"regexp"
	"strings"
	"time"
)

type linearActivity struct {
	ID, Request, Identity, Session, Actor, Semantic, Owner string
	Content                                                json.RawMessage
	Started                                                bool
	Attempts                                               int
}
type linearActivityReceipt struct {
	ID      string `json:"id"`
	Session struct {
		ID string `json:"id"`
	} `json:"agentSession"`
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	Content json.RawMessage `json:"content"`
}

const activityFields = `id agentSession { id } user { id } content { ... on AgentActivityThoughtContent { type body } ... on AgentActivityElicitationContent { type body } ... on AgentActivityResponseContent { type body } ... on AgentActivityErrorContent { type body } }`

var linearActivityLink = regexp.MustCompile(`\]\(<(https?://[^<>\s]+)>\)`)

func (s *Service) prepareLinearActivities(ctx context.Context) error {
	// Status synchronization never locks a run, preserving the worker/cancel lock order.
	_, err := s.pool.Exec(ctx, `UPDATE external_requests q SET status=CASE r.status WHEN 'cancelled' THEN 'stopped' WHEN 'provisioning' THEN 'queued' WHEN 'finalizing' THEN 'running' WHEN 'waiting_for_approval' THEN 'running' WHEN 'waiting_for_input' THEN 'running' ELSE r.status END,updated_at=now() FROM runs r WHERE q.run_id=r.id AND q.stopped_at IS NULL AND q.status<>CASE r.status WHEN 'cancelled' THEN 'stopped' WHEN 'provisioning' THEN 'queued' WHEN 'finalizing' THEN 'running' WHEN 'waiting_for_approval' THEN 'running' WHEN 'waiting_for_input' THEN 'running' ELSE r.status END`)
	if err != nil {
		return err
	}
	// Coalesce obsolete unsent progress. Started writes always reconcile their receipt.
	_, err = s.pool.Exec(ctx, `UPDATE linear_agent_activities a SET status='cancelled' FROM external_requests q WHERE a.request_id=q.id AND NOT a.started AND a.status='pending' AND (q.status IN ('succeeded','failed','stopped')) AND (a.semantic_key IN ('ack','request:queued','request:running') OR a.semantic_key LIKE 'heartbeat:%' OR a.semantic_key LIKE '%:running')`)
	if err != nil {
		return err
	}
	// A single operational heartbeat per minute. The semantic key is stable across consumers.
	_, err = s.pool.Exec(ctx, `SELECT queue_native_linear_activity(q.id,'heartbeat:'||to_char(now() AT TIME ZONE 'UTC','YYYYMMDDHH24MI')) FROM external_requests q WHERE q.status='running' AND q.stopped_at IS NULL AND NOT EXISTS(SELECT 1 FROM linear_agent_activities a WHERE a.request_id=q.id AND a.created_at>now()-interval '1 minute')`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `SELECT queue_native_linear_activity(s.request_id,'pr:'||d.run_id::text) FROM external_session_runs s JOIN github_run_deliveries d ON d.run_id=s.run_id JOIN external_requests q ON q.id=s.request_id WHERE d.status='delivered' AND q.stopped_at IS NULL`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `SELECT queue_native_linear_activity(s.request_id,'review:'||p.id::text) FROM external_session_runs s JOIN pr_reviews p ON p.run_id=s.run_id JOIN external_requests q ON q.id=s.request_id JOIN pr_review_publications pub ON pub.review_id=p.id WHERE pub.status='published' AND q.stopped_at IS NULL`)
	return err
}
func (s *Service) ProcessLinearActivity(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := s.prepareLinearActivities(ctx); err != nil {
		return false, err
	}
	a := linearActivity{Owner: uuid.NewString()}
	err := s.pool.QueryRow(ctx, `WITH candidate AS(SELECT id FROM linear_agent_activities WHERE status IN ('pending','uncertain') AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY CASE WHEN semantic_key='request:stopped' THEN 0 WHEN semantic_key='ack' THEN 1 ELSE 2 END,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE linear_agent_activities a SET lease_owner=$1,lease_until=now()+interval '30 seconds',attempts=a.attempts+1 FROM candidate c WHERE a.id=c.id RETURNING a.id,a.request_id,a.identity_id,a.session_id,a.actor_id,a.semantic_key,a.content,a.started,a.attempts`, a.Owner).Scan(&a.ID, &a.Request, &a.Identity, &a.Session, &a.Actor, &a.Semantic, &a.Content, &a.Started, &a.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	detail, err := s.ExternalRequest(ctx, a.Request)
	if err != nil {
		return true, err
	}
	if len(a.Content) == 0 {
		a.Content = s.activityContent(ctx, a, detail)
		tag, e := s.pool.Exec(ctx, `UPDATE linear_agent_activities SET content=$3 WHERE id=$1 AND lease_owner=$2 AND lease_until>now()`, a.ID, a.Owner, a.Content)
		if e != nil {
			return true, e
		}
		if tag.RowsAffected() == 0 {
			return true, nil
		}
	}
	status, reason := "delivered", ""
	retryAt := time.Now().Add(time.Minute)
	clearStarted := false
	err = s.withLinearIdentity(ctx, a.Identity, func(token string) error {
		who, e := s.linearActor(ctx, token)
		if e != nil {
			return e
		}
		var account string
		if e = s.pool.QueryRow(ctx, `SELECT account_id FROM provider_identities WHERE id=$1 AND granted_scopes @> ARRAY['app:mentionable','app:assignable']`, a.Identity).Scan(&account); e != nil {
			return ErrIdentityUnavailable
		}
		if who.Viewer.ID != a.Actor || who.Organization.ID != account {
			return ErrAccess
		}
		if a.Started {
			receipt, e := s.linearActivityReceipt(ctx, token, a)
			if e != nil {
				return e
			}
			if receipt == nil {
				return errPublicationUncertain
			}
		} else {
			// Reservation is durable before POST; stop handling shares this request lock.
			reserved, e := s.reserveLinearActivity(ctx, a)
			if e != nil {
				return e
			}
			if !reserved {
				status = "cancelled"
				return nil
			}
			a.Started = true
			var response struct {
				Create struct {
					Success  bool                  `json:"success"`
					Activity linearActivityReceipt `json:"agentActivity"`
				} `json:"agentActivityCreate"`
			}
			e = s.linear(ctx, token, `mutation CircularAgentActivity($input: AgentActivityCreateInput!) { agentActivityCreate(input: $input) { success agentActivity { `+activityFields+` } } }`, map[string]any{"input": map[string]any{"id": a.ID, "agentSessionId": a.Session, "content": a.Content}}, &response)
			if errors.Is(e, ErrAccess) || errors.Is(e, ErrReconnect) {
				clearStarted = true
			}
			var limited *providerRetry
			if errors.As(e, &limited) {
				clearStarted = true
			}
			if e != nil {
				return e
			}
			if !response.Create.Success || !activityReceiptMatches(response.Create.Activity, a) {
				return errPublicationUncertain
			}
		}
		allowed, e := s.reserveLinearSessionLinks(ctx, a)
		if e != nil {
			return e
		}
		if !allowed {
			return nil
		}
		urls := []map[string]string{{"label": "Circular request", "url": s.config.WebURL + "/requests/" + a.Request}}
		if detail.RunURL != "" {
			urls = append(urls, map[string]string{"label": "Circular run", "url": detail.RunURL})
		}
		if detail.PullRequestURL != "" {
			urls = append(urls, map[string]string{"label": "Pull request", "url": detail.PullRequestURL})
		}
		var linked struct {
			Update struct {
				Success bool `json:"success"`
			} `json:"agentSessionUpdate"`
		}
		e = s.linear(ctx, token, `mutation CircularSessionLinks($id: String!, $input: AgentSessionUpdateInput!) { agentSessionUpdate(id: $id, input: $input) { success } }`, map[string]any{"id": a.Session, "input": map[string]any{"externalUrls": urls}}, &linked)
		if e == nil && !linked.Update.Success {
			return ErrProvider
		}
		return e
	})
	if errors.Is(err, errPublicationUncertain) {
		status, reason = "uncertain", "Checking whether Linear received this activity; a second activity will not be created."
	} else if err != nil {
		status, reason = "pending", "Linear delivery is paused. Circular will retry using the same activity identity."
		var limited *providerRetry
		if errors.As(err, &limited) {
			retryAt = time.Now().Add(limited.delay)
		}
		if errors.Is(err, ErrAccess) || errors.Is(err, ErrReconnect) || errors.Is(err, ErrIdentityUnavailable) {
			reason = "Restore Circular's Linear agent access to resume delivery."
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE linear_agent_activities SET status=$3,last_error=$4,receipt_id=CASE WHEN $3='delivered' THEN id::text ELSE receipt_id END,started=CASE WHEN $6 THEN false ELSE started END,lease_owner=NULL,lease_until=NULL,next_attempt_at=$5,updated_at=now() WHERE id=$1 AND lease_owner=$2 AND lease_until>now()`, a.ID, a.Owner, status, reason, retryAt, clearStarted)
	return true, err
}
func (s *Service) reserveLinearActivity(ctx context.Context, a linearActivity) (bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer rollback(ctx, tx)
	var stopped *time.Time
	if e = tx.QueryRow(ctx, `SELECT stopped_at FROM external_requests WHERE id=$1 FOR NO KEY UPDATE`, a.Request).Scan(&stopped); e != nil {
		return false, e
	}
	if stopped != nil && a.Semantic != "request:stopped" && !strings.HasPrefix(a.Semantic, "message:") {
		return false, nil
	}
	tag, e := tx.Exec(ctx, `UPDATE linear_agent_activities SET started=true WHERE id=$1 AND lease_owner=$2 AND lease_until>now() AND status='pending'`, a.ID, a.Owner)
	if e != nil {
		return false, e
	}
	if e = tx.Commit(ctx); e != nil {
		return false, e
	}
	return tag.RowsAffected() == 1, nil
}
func activityReceiptMatches(r linearActivityReceipt, a linearActivity) bool {
	if r.ID != a.ID || r.Session.ID != a.Session || r.User.ID != a.Actor {
		return false
	}
	var got, want map[string]any
	if json.Unmarshal(r.Content, &got) != nil || json.Unmarshal(a.Content, &want) != nil {
		return false
	}
	delete(got, "__typename")
	// Linear rewrites link brackets and unordered-list markers. Other content
	// and the activity's stable identity must still match.
	for _, content := range []map[string]any{got, want} {
		if body, ok := content["body"].(string); ok {
			content["body"] = normalizeLinearActivityBody(body)
		}
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	return string(g) == string(w)
}

func normalizeLinearActivityBody(body string) string {
	body = linearActivityLink.ReplaceAllString(body, "]($1)")
	lines := strings.Split(body, "\n")
	// HTML can contain literal list-looking text. Leave these bodies strict
	// rather than trying to interpret HTML within the receipt comparison.
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "<") {
			return body
		}
	}
	var fence byte
	var fenceLength int
	for i, line := range lines {
		text := strings.TrimLeft(line, " ")
		if len(line)-len(text) > 3 || strings.HasPrefix(text, "\t") {
			continue
		}
		if fence != 0 {
			n := len(text) - len(strings.TrimLeft(text, string(fence)))
			if n >= fenceLength && strings.Trim(strings.TrimSuffix(text[n:], "\r"), " \t") == "" {
				fence = 0
			}
			continue
		}
		if strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") {
			fence = text[0]
			fenceLength = len(text) - len(strings.TrimLeft(text, string(fence)))
			continue
		}
		// Only the observed top-level marker rewrite is allowed. Indentation,
		// item text, whitespace, and thematic breaks remain significant.
		if strings.HasPrefix(line, "- ") {
			if strings.Trim(line[2:], " \t\r") == "" {
				continue
			}
			marks := strings.NewReplacer(" ", "", "\t", "").Replace(strings.TrimSuffix(line, "\r"))
			if len(marks) >= 3 && (strings.Trim(marks, "-") == "" || strings.Trim(marks[1:], "*") == "") {
				continue
			}
			lines[i] = "* " + line[2:]
		}
	}
	return strings.Join(lines, "\n")
}

func (s *Service) linearActivityReceipt(ctx context.Context, token string, a linearActivity) (*linearActivityReceipt, error) {
	var response struct {
		Activities struct {
			Nodes []linearActivityReceipt `json:"nodes"`
		} `json:"agentActivities"`
	}
	e := s.linear(ctx, token, `query CircularAgentActivityReceipt($id: ID!) { agentActivities(first: 1, filter: { id: { eq: $id } }) { nodes { `+activityFields+` } } }`, map[string]any{"id": a.ID}, &response)
	if e != nil {
		return nil, e
	}
	if len(response.Activities.Nodes) != 1 || !activityReceiptMatches(response.Activities.Nodes[0], a) {
		return nil, nil
	}
	return &response.Activities.Nodes[0], nil
}
func (s *Service) activityContent(ctx context.Context, a linearActivity, r ExternalRequestDetail) json.RawMessage {
	kind, body := "thought", "Circular received this request."
	name := strings.Map(func(c rune) rune {
		if c == '\n' || c == '\r' || c == '[' || c == ']' {
			return ' '
		}
		return c
	}, r.AgentName)
	if name == "" {
		name = "Circular"
	}
	switch r.Status {
	case "needs_routing", "needs_access", "awaiting_approval", "waiting_for_active_run":
		kind = "elicitation"
		body = r.Reason
	case "unsupported", "rejected", "failed":
		kind = "error"
		body = r.Reason
		if body == "" {
			body = name + " could not complete this run. Review its result in Circular."
		}
	case "queued":
		body = name + " is queued in Circular."
	case "running":
		body = name + " is working in Circular."
	case "succeeded":
		kind = "response"
		body = name + " succeeded. Review the changes and checks in Circular."
	case "stopped":
		kind = "response"
		body = "Circular stopped this request. Any provider write already in flight may still appear; its receipt will be checked."
	}
	if strings.HasPrefix(a.Semantic, "message:") {
		kind = "response"
		body = "Additional instructions are saved. They do not change this run. Start a new Linear session for new work."
		if r.Status == "stopped" {
			body += " This request remains stopped."
		}
	}
	if strings.HasPrefix(a.Semantic, "run:") {
		parts := strings.Split(a.Semantic, ":")
		if len(parts) == 3 && parts[1] != r.RunID {
			kind = "response"
			body = "The PR review " + parts[2] + ". Open Circular for the review report."
		}
	}
	if strings.HasPrefix(a.Semantic, "pr:") {
		kind = "response"
		body = name + " published the draft pull request."
	}
	if strings.HasPrefix(a.Semantic, "review:") {
		kind = "response"
		body = "Circular posted the PR review. Open the review report for findings and checks."
	}
	if r.Status == "succeeded" && a.Semantic == "request:succeeded" && r.RunID != "" {
		var summary string
		if err := s.pool.QueryRow(ctx, `SELECT left(data->>'content',1200) FROM events WHERE run_id=$1 AND type='agent.message.completed' ORDER BY sequence DESC LIMIT 1`, r.RunID).Scan(&summary); err == nil && summary != "" {
			body += "\n\n" + summary
		}
	}
	if r.RunURL != "" {
		body += "\n\n[Open run](" + r.RunURL + ")"
	}
	if r.PullRequestURL != "" {
		body += "\n\n[Open pull request](" + r.PullRequestURL + ")"
	}
	body += "\n\n[Open request](" + s.config.WebURL + "/requests/" + r.ID + ")"
	if len(body) > 8000 {
		body = fmt.Sprintf("Circular request is %s. Open Circular for details.", r.Status)
	}
	raw, _ := json.Marshal(map[string]string{"type": kind, "body": body})
	return raw
}

// Each idempotent link update still reserves its write before the HTTP call.
func (s *Service) reserveLinearSessionLinks(ctx context.Context, a linearActivity) (bool, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer rollback(ctx, tx)
	var stopped bool
	if e = tx.QueryRow(ctx, `SELECT stopped_at IS NOT NULL FROM external_requests WHERE id=$1 FOR NO KEY UPDATE`, a.Request).Scan(&stopped); e != nil {
		return false, e
	}
	if stopped && a.Semantic != "request:stopped" {
		return false, nil
	}
	if _, e = tx.Exec(ctx, `INSERT INTO external_effect_reservations(id,request_id,run_id,effect) SELECT $1,id,run_id,$3 FROM external_requests WHERE id=$2`, uuid.New(), a.Request, "linear_session_links:"+a.ID); e != nil {
		return false, e
	}
	return true, tx.Commit(ctx)
}
