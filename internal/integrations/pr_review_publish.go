package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

type reviewPublication struct {
	ID, Owner            uuid.UUID
	Marker, Author, Body string
	Started              bool
	Attempts             int
	Publisher            *PublisherIdentity
}

func (s *Service) RetryPRReviewPublication(ctx context.Context, id string) (prreviews.Review, error) {
	r, err := s.PRReview(ctx, id)
	if err != nil {
		return r, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE pr_review_publications SET status='retrying',next_attempt_at=now(),updated_at=now() WHERE review_id=$1 AND status IN ('failed','retrying','uncertain') AND (lease_until IS NULL OR lease_until<now())`, r.ID)
	if err != nil {
		return r, err
	}
	return s.PRReview(ctx, id)
}
func (s *Service) ProcessPRReviewPublication(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	p := reviewPublication{Owner: uuid.New()}
	var publisher []byte
	err := s.pool.QueryRow(ctx, `WITH candidate AS (SELECT review_id FROM pr_review_publications WHERE status IN ('pending','retrying') AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE pr_review_publications p SET lease_owner=$1,lease_until=now()+interval '2 minutes',attempts=LEAST(attempts+1,1000000) FROM candidate c WHERE p.review_id=c.review_id RETURNING p.review_id,p.marker,p.expected_author_id,p.body,p.started,p.attempts,p.publisher`, p.Owner).Scan(&p.ID, &p.Marker, &p.Author, &p.Body, &p.Started, &p.Attempts, &publisher)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	r, err := s.PRReview(ctx, p.ID.String())
	if err != nil {
		return true, err
	}
	finish := func(status, message string, delay time.Duration, receipt *githubReviewReceipt) error {
		record, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		tx, err := s.pool.Begin(record)
		if err != nil {
			return err
		}
		defer rollback(record, tx)
		id, link := "", ""
		if receipt != nil {
			id, link = receipt.ID, receipt.HTMLURL
		}
		command, err := tx.Exec(record, `UPDATE pr_review_publications SET status=$3,last_error=$4,next_attempt_at=now()+$5::interval,github_review_id=CASE WHEN $6='' THEN github_review_id ELSE $6 END,github_review_url=CASE WHEN $7='' THEN github_review_url ELSE $7 END,lease_owner=NULL,lease_until=NULL,updated_at=now() WHERE review_id=$1 AND lease_owner=$2 AND lease_until>now()`, p.ID, p.Owner, status, message, fmt.Sprintf("%f seconds", delay.Seconds()), id, link)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return prreviews.ErrReviewUnavailable
		}
		if link != "" {
			if err = s.attachReviewLinearURL(record, tx, r.RunID, link); err != nil {
				return err
			}
		}
		return tx.Commit(record)
	}
	if len(publisher) > 0 && (json.Unmarshal(publisher, &p.Publisher) != nil || p.Publisher == nil || !p.Publisher.valid("github")) {
		return true, finish("failed", "Saved publication identity is invalid", 0, nil)
	}
	// A started request is reconciled first, even if code has moved or retained
	// bytes are now unavailable. It must never become another POST.
	if p.Started {
		var receipt *githubReviewReceipt
		err = s.withGitHubRepository(ctx, r.Snapshot.ProjectID.String(), r.Snapshot.RepositoryID.String(), GitHubReviewRead, func(token string, actor PublisherIdentity) error {
			d := githubDelivery{GitHubRepository: r.Snapshot.PR.GitHubRepositoryID, Installation: r.Snapshot.PR.InstallationID, Name: r.Snapshot.PR.RepositoryName, CredentialApp: actor.Mode == "app"}
			if err := s.deliveryRepository(ctx, token, &d); err != nil {
				return err
			}
			var err error
			receipt, err = s.findReviewReceipt(ctx, token, r, p)
			return err
		})
		if err == nil && receipt != nil {
			return true, finish("published", "", 0, receipt)
		}
		return true, finish("uncertain", "GitHub may have received this review. Retry checks for its receipt; it will not post a second review.", retryDelay(p.Attempts), nil)
	}
	if err = s.retainedReviewReport(ctx, &r); err != nil {
		return true, finish("failed", "The successful review report could not be verified from retained artifacts.", 0, nil)
	}
	var outcome = "retrying"
	message := "GitHub is unavailable. Check the connection and repository permissions."
	delay := retryDelay(p.Attempts)
	var receipt *githubReviewReceipt
	if p.Publisher == nil {
		identity, err := s.PublicationIdentity(ctx, r.Snapshot.ProjectID.String(), "github", r.Snapshot.PR.InstallationID)
		if err != nil {
			return true, finish("retrying", err.Error(), delay, nil)
		}
		if p.Author != "" && p.Author != identity.ActorID {
			return true, finish("uncertain", "The original review author needs to be reconnected", 0, nil)
		}
		p.Publisher = &identity
		p.Author = identity.ActorID
		raw, _ := json.Marshal(identity)
		command, err := s.pool.Exec(ctx, `UPDATE pr_review_publications SET publisher=$3,expected_author_id=$4 WHERE review_id=$1 AND lease_owner=$2 AND lease_until>now() AND publisher IS NULL AND NOT started`, p.ID, p.Owner, raw, p.Author)
		if err != nil {
			return true, err
		}
		if command.RowsAffected() != 1 {
			return true, prreviews.ErrReviewUnavailable
		}
	}
	err = s.withGitHubPublisher(ctx, r.Snapshot.ProjectID.String(), r.Snapshot.RepositoryID.String(), GitHubReviewWrite, *p.Publisher, false, func(token string, actor PublisherIdentity) error {
		owner, claimed, err := s.claimReviewFreshness(ctx, r, true)
		if err != nil {
			return err
		}
		if !claimed {
			message = "Another check is verifying this PR. Publication will retry shortly."
			delay = 30 * time.Second
			return nil
		}
		operation, stop := context.WithTimeout(ctx, 25*time.Second)
		live, state, checkErr := s.inspectReviewPR(operation, token, r.Snapshot, actor)
		stop()
		if err = s.recordReviewFreshness(ctx, r, owner, live, state, checkErr); err != nil {
			return err
		}
		if checkErr != nil {
			return checkErr
		}
		if state != "open" || live.BaseSHA != r.Snapshot.PR.BaseSHA || live.HeadSHA != r.Snapshot.PR.HeadSHA {
			outcome = "skipped"
			message = "The PR closed or its base/head changed after this review. Start a new review for the current code."
			return nil
		}
		p.Body, err = reviewBody(r, s.config.WebURL)
		if err != nil {
			return err
		}
		p.Author = p.Publisher.ActorID
		reserved, err := s.reserveReviewPublication(ctx, r.RunID, p)
		if errors.Is(err, postgres.ErrExternalStopped) {
			outcome = "skipped"
			message = err.Error()
			return nil
		}
		if err != nil {
			return err
		}
		if !reserved {
			return prreviews.ErrReviewUnavailable
		}

		p.Started = true
		payload, _ := json.Marshal(map[string]string{"body": p.Body, "event": "COMMENT", "commit_id": r.Snapshot.PR.HeadSHA})
		var value githubReviewReceipt
		status, headers, err := s.reviewRequest(ctx, token, http.MethodPost, reviewEndpoint(r), payload, &value)
		if err == nil && (status == 200 || status == 201) && matchesReviewReceipt(value, r, p.Marker, p.Author) && value.Body == p.Body {
			outcome = "published"
			message = ""
			receipt = &value
			return nil
		}
		definitive := err == nil && (status == 400 || status == 401 || status == 403 || status == 404 || status == 422 || status == 429)
		if definitive {
			command, err := s.pool.Exec(ctx, `UPDATE pr_review_publications SET started=false WHERE review_id=$1 AND lease_owner=$2 AND lease_until>now()`, p.ID, p.Owner)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return prreviews.ErrReviewUnavailable
			}
			p.Started = false
			delay = reviewRetryDelay(headers, delay)
			message = "GitHub rejected this review. Check repository permissions and retry publication."
			return nil
		}
		outcome = "uncertain"
		message = "GitHub may have received this review. Retry checks for its receipt; it will not post a second review."
		return nil
	})
	if err != nil && p.Started {
		outcome = "uncertain"
		message = "GitHub may have received this review. Retry checks for its receipt; it will not post a second review."
	}
	return true, finish(outcome, message, providerRetryDelay(err, delay), receipt)
}
func reviewEndpoint(r prreviews.Review) string {
	return "/repos/" + r.Snapshot.PR.RepositoryName + "/pulls/" + strconv.Itoa(r.Snapshot.PR.Number) + "/reviews"
}
func (s *Service) retainedReviewReport(ctx context.Context, r *prreviews.Review) error {
	if r.RunStatus != runstate.Succeeded || r.Report == nil || r.ReportError != "" {
		return prreviews.ErrReviewUnavailable
	}
	var content artifacts.Content
	var reportSHA string
	var valid bool
	err := s.pool.QueryRow(ctx, `SELECT a.uri,(a.metadata->>'size_bytes')::bigint,a.metadata->>'sha256',p.report_sha256,a.id=$2 AND a.kind='pr_review_report' AND a.run_id=p.run_id AND a.metadata->>'review_id'=p.id::text AND repo.project_id=p.project_id AND repo.external_refs->'github'->>'repository_id'=p.snapshot->'pr'->>'github_repository_id' AND repo.external_refs->'github'->>'installation_id'=p.snapshot->'pr'->>'installation_id' FROM pr_reviews p JOIN artifacts a ON a.id=p.report_artifact_id JOIN repositories repo ON repo.id=p.repository_id WHERE p.id=$1`, r.ID, artifacts.PRReviewID(r.RunID, "pr-review-report.json")).Scan(&content.URI, &content.SizeBytes, &content.SHA256, &reportSHA, &valid)
	if err != nil || !valid || content.SHA256 != reportSHA || content.SizeBytes > prreviews.MaxReportBytes {
		return prreviews.ErrInvalidReport
	}
	store, err := artifacts.NewLocalStore(s.config.ArtifactRoot)
	if err != nil {
		return err
	}
	if err = store.Verify(ctx, r.RunID, content); err != nil {
		return err
	}
	raw, err := store.ReadBounded(ctx, r.RunID, content.URI, prreviews.MaxReportBytes)
	if err != nil {
		return err
	}
	report, err := prreviews.DecodeReport(raw)
	if err != nil {
		return err
	}
	hash, err := prreviews.Fingerprint(report)
	if err != nil || hash != reportSHA {
		return prreviews.ErrInvalidReport
	}
	r.Report = &report
	if prreviews.Assess(r.RunStatus, &prreviews.ValidatedReport{Report: report}) != r.Assessment {
		return prreviews.ErrInvalidReport
	}
	return nil
}

// Preserve response headers for rate-limit guidance and validate every next link
// before passing the credential to it. Redirects are disabled by Service.New.
func (s *Service) reviewRequest(ctx context.Context, token, method, path string, body []byte, out any) (int, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.config.GitHubAPIURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, ErrProvider
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	req.Header.Set("User-Agent", "Circular")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := s.client.Do(req)
	if err != nil {
		return 0, nil, ErrProvider
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, response.Header, nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(raw) > 4<<20 || json.Unmarshal(raw, out) != nil {
		return response.StatusCode, response.Header, ErrProvider
	}
	return response.StatusCode, response.Header, nil
}
func (s *Service) findReviewReceipt(ctx context.Context, token string, r prreviews.Review, p reviewPublication) (*githubReviewReceipt, error) {
	endpoint := reviewEndpoint(r)
	path := endpoint + "?per_page=100&page=1"
	seen := map[string]bool{}
	for range 1000 {
		if seen[path] {
			return nil, ErrProvider
		}
		seen[path] = true
		var values []githubReviewReceipt
		status, headers, err := s.reviewRequest(ctx, token, http.MethodGet, path, nil, &values)
		if err != nil || status != 200 {
			return nil, ErrProvider
		}
		for _, value := range values {
			if matchesReviewReceipt(value, r, p.Marker, p.Author) && value.Body == p.Body {
				return &value, nil
			}
		}
		next := ""
		for _, part := range strings.Split(headers.Get("Link"), ",") {
			if !strings.Contains(part, `rel="next"`) {
				continue
			}
			left, right := strings.Index(part, "<"), strings.Index(part, ">")
			if left < 0 || right <= left {
				return nil, ErrProvider
			}
			next = part[left+1 : right]
		}
		if next == "" {
			return nil, nil
		}
		target, err := url.Parse(next)
		base, e := url.Parse(s.config.GitHubAPIURL)
		if err != nil || e != nil || target.Scheme != base.Scheme || target.Host != base.Host || target.User != nil || target.Fragment != "" || target.Path != base.Path+endpoint {
			return nil, ErrProvider
		}
		query := target.Query()
		if len(query) != 2 || query.Get("per_page") != "100" || !positiveNumber(query.Get("page")) {
			return nil, ErrProvider
		}
		path = endpoint + "?" + query.Encode()
	}
	return nil, ErrProvider
}
func reviewRetryDelay(headers http.Header, fallback time.Duration) time.Duration {
	delay := fallback
	if seconds, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && seconds > 0 {
		delay = max(delay, time.Duration(min(seconds, 86400))*time.Second)
	} else if when, err := http.ParseTime(headers.Get("Retry-After")); err == nil {
		delay = max(delay, time.Until(when))
	}
	if epoch, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		delay = max(delay, time.Until(time.Unix(epoch, 0)))
	}
	return min(max(delay, 30*time.Second), 24*time.Hour)
}

// Provider retry timing carries no response body or credential material.
type providerRetry struct{ delay time.Duration }

func (e *providerRetry) Error() string { return ErrProvider.Error() }
func (e *providerRetry) Unwrap() error { return ErrProvider }
func providerRetryError(status int, headers http.Header) error {
	if status == 429 || status == 403 && (headers.Get("Retry-After") != "" || headers.Get("X-RateLimit-Remaining") == "0") {
		return &providerRetry{reviewRetryDelay(headers, time.Minute)}
	}
	return nil
}
func providerRetryDelay(err error, fallback time.Duration) time.Duration {
	var limited *providerRetry
	if errors.As(err, &limited) {
		return max(fallback, limited.delay)
	}
	return fallback
}
