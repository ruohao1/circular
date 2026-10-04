package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

func (s *Service) reviewer(ctx context.Context, project string, id uuid.UUID) (prreviews.ReviewerSnapshot, error) {
	var v prreviews.ReviewerSnapshot
	var enabled bool
	err := s.pool.QueryRow(ctx, `SELECT id,name,backend,instructions,backend_config,enabled FROM agents WHERE id=$1 AND project_id=$2`, id, project).Scan(&v.AgentID, &v.Name, &v.Backend, &v.Instructions, &v.BackendConfig, &enabled)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !enabled {
		return v, prreviews.ErrReviewUnavailable
	}
	if err != nil {
		return v, err
	}
	return prreviews.NormalizeReviewer(v)
}
func (s *Service) PRReviewSettings(ctx context.Context, project string) (prreviews.Settings, error) {
	var v prreviews.Settings
	err := s.pool.QueryRow(ctx, `SELECT automatic,reviewer_id,(SELECT count(*) FROM pr_review_launch_intents WHERE project_id=$1 AND status='pending') FROM pr_review_settings WHERE project_id=$1`, project).Scan(&v.Automatic, &v.ReviewerID, &v.PendingCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrAccess
	}
	if err != nil {
		return v, err
	}
	if v.ReviewerID == nil {
		v.UnavailableReason = "Choose a PR reviewer."
		return v, nil
	}
	reviewer, err := s.reviewer(ctx, project, *v.ReviewerID)
	if err == nil {
		v.Reviewer = &reviewer
		err = s.githubProjectPermission(ctx, project)
	}
	if err != nil {
		if reviewAvailabilityError(err) {
			v.UnavailableReason = err.Error()
			return v, nil
		}
		return v, err
	}
	v.Available = true
	return v, nil
}
func reviewAvailabilityError(err error) bool {
	for _, target := range []error{prreviews.ErrReviewUnavailable, prreviews.ErrSourceInvalid, ErrDeliveryPermission, ErrReconnect, ErrConfiguration, ErrAccess, ErrProvider, ErrIdentityUnavailable, ErrIdentityConflict, errDeliveryConflict} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
func (s *Service) SetPRReviewSettings(ctx context.Context, project string, update prreviews.SettingsUpdate) (prreviews.Settings, error) {
	var selected *uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT reviewer_id FROM pr_review_settings WHERE project_id=$1`, project).Scan(&selected); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return prreviews.Settings{}, ErrAccess
		}
		return prreviews.Settings{}, err
	}
	if update.ReviewerID != nil {
		selected = update.ReviewerID
	}
	if update.Automatic || update.ReviewerID != nil {
		if selected == nil {
			return prreviews.Settings{}, prreviews.ErrReviewUnavailable
		}
		if _, err := s.reviewer(ctx, project, *selected); err != nil {
			return prreviews.Settings{}, err
		}
	}
	if update.Automatic {
		if err := s.githubProjectPermission(ctx, project); err != nil {
			return prreviews.Settings{}, err
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return prreviews.Settings{}, err
	}
	defer rollback(ctx, tx)
	// Updating the settings first serializes all automatic launch/disable operations.
	_, err = tx.Exec(ctx, `UPDATE pr_review_settings SET automatic=$2,reviewer_id=CASE WHEN $3::uuid IS NULL THEN reviewer_id ELSE $3 END,updated_at=now() WHERE project_id=$1`, project, update.Automatic, update.ReviewerID)
	if err != nil {
		return prreviews.Settings{}, err
	}
	if !update.Automatic {
		_, err = tx.Exec(ctx, `UPDATE pr_review_launch_intents SET status='cancelled',lease_owner=NULL,lease_until=NULL,last_error='Automatic PR reviews were turned off before launch.' WHERE project_id=$1 AND automatic AND status='pending'`, project)
		if err != nil {
			return prreviews.Settings{}, err
		}
		rows, err := tx.Query(ctx, `SELECT r.id FROM runs r JOIN pr_reviews p ON p.run_id=r.id WHERE p.project_id=$1 AND p.automatic AND r.status='queued' ORDER BY r.id FOR UPDATE OF r`, project)
		if err != nil {
			return prreviews.Settings{}, err
		}
		var ids []uuid.UUID
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return prreviews.Settings{}, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return prreviews.Settings{}, err
		}
		for _, id := range ids {
			if _, err = tx.Exec(ctx, `UPDATE runs SET status='cancelled',finished_at=clock_timestamp(),updated_at=now() WHERE id=$1`, id); err != nil {
				return prreviews.Settings{}, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) SELECT $1,$2,COALESCE(MAX(sequence),0)+1,'run.cancelled','api','{}',clock_timestamp() FROM events WHERE run_id=$2`, uuid.New(), id); err != nil {
				return prreviews.Settings{}, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return prreviews.Settings{}, err
	}
	return s.PRReviewSettings(ctx, project)
}

// inspectReviewPR verifies installation visibility and both numeric repository identities.
func (s *Service) inspectReviewPR(ctx context.Context, token string, snapshot prreviews.LaunchSnapshot, actor PublisherIdentity) (prreviews.PRIdentity, string, error) {
	v := snapshot.PR
	if !positiveNumber(v.GitHubRepositoryID) || !positiveNumber(v.InstallationID) || v.Number < 1 {
		return v, "", prreviews.ErrSourceInvalid
	}
	if actor.Mode != "app" {
		if err := s.deliveryPermission(ctx, token, v.InstallationID); err != nil {
			return v, "", err
		}
	}
	d := githubDelivery{GitHubRepository: v.GitHubRepositoryID, Installation: v.InstallationID, Name: v.RepositoryName, CredentialApp: actor.Mode == "app"}
	if err := s.deliveryRepository(ctx, token, &d); err != nil {
		return v, "", err
	}
	var pr struct {
		Number     int    `json:"number"`
		URL        string `json:"html_url"`
		State      string `json:"state"`
		Merged     bool   `json:"merged"`
		Base, Head struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				ID json.Number `json:"id"`
			} `json:"repo"`
		}
	}
	status, err := s.githubRequest(ctx, token, http.MethodGet, "/repos/"+v.RepositoryName+"/pulls/"+strconv.Itoa(v.Number), nil, &pr)
	if err != nil {
		return v, "", err
	}
	if status != 200 {
		return v, "", deliveryHTTPError(status)
	}
	if pr.Number != v.Number || pr.URL != "https://github.com/"+v.RepositoryName+"/pull/"+strconv.Itoa(v.Number) || string(pr.Base.Repo.ID) != v.GitHubRepositoryID || string(pr.Head.Repo.ID) != v.GitHubRepositoryID || !prreviews.CommitPattern.MatchString(pr.Base.SHA) || !prreviews.CommitPattern.MatchString(pr.Head.SHA) || pr.Base.Ref == "" || pr.Head.Ref == "" {
		return v, "", prreviews.ErrSourceInvalid
	}
	if pr.State != "open" && pr.State != "closed" {
		return v, "", prreviews.ErrSourceInvalid
	}
	v.BaseSHA, v.HeadSHA, v.BaseRef, v.HeadRef, v.URL = pr.Base.SHA, pr.Head.SHA, pr.Base.Ref, pr.Head.Ref, pr.URL
	state := pr.State
	if pr.Merged {
		state = "merged"
	}
	return v, state, nil
}
func (s *Service) prepareReview(ctx context.Context, source, reviewer uuid.UUID) (prreviews.LaunchSnapshot, error) {
	value, err := postgres.NewPRReviewStore(s.pool).Source(ctx, source, reviewer)
	if err != nil {
		return value, err
	}
	err = s.withGitHubRepository(ctx, value.ProjectID.String(), value.RepositoryID.String(), GitHubReviewRead, func(token string, actor PublisherIdentity) error {
		var state string
		var err error
		value.PR, state, err = s.inspectReviewPR(ctx, token, value, actor)
		if err != nil {
			return err
		}
		if state != "open" {
			return fmt.Errorf("%w: this pull request is %s", prreviews.ErrReviewUnavailable, state)
		}
		return nil
	})
	if err != nil {
		return value, err
	}
	return prreviews.SealSnapshot(value)
}
func (s *Service) PreparePRReview(ctx context.Context, source, reviewer string) (prreviews.Preparation, error) {
	var v prreviews.Preparation
	sourceID, err := uuid.Parse(source)
	if err != nil {
		return v, ErrAccess
	}
	var reviewerID uuid.UUID
	if reviewer != "" {
		reviewerID, err = uuid.Parse(reviewer)
		if err != nil {
			return v, ErrAccess
		}
	}
	v.Snapshot, err = s.prepareReview(ctx, sourceID, reviewerID)
	if err != nil {
		if reviewAvailabilityError(err) {
			v.Snapshot = prreviews.LaunchSnapshot{Evidence: []prreviews.Evidence{}, TaskExternalRefs: json.RawMessage(`{}`), Reviewer: prreviews.ReviewerSnapshot{BackendConfig: json.RawMessage(`{}`)}}
			v.Reason = err.Error()
			return v, nil
		}
		return v, err
	}
	v.Ready = true
	return v, nil
}
func (s *Service) LaunchPRReview(ctx context.Context, source string, request prreviews.LaunchRequest) (prreviews.Review, error) {
	var zero prreviews.Review
	if err := request.Validate(); err != nil {
		return zero, err
	}
	id, err := uuid.Parse(source)
	if err != nil {
		return zero, ErrAccess
	}
	store := postgres.NewPRReviewStore(s.pool)
	if replay, err := store.Replay(ctx, id, request); err != nil {
		return zero, err
	} else if replay != nil {
		return *replay, nil
	}
	snapshot, err := s.prepareReview(ctx, id, request.ReviewerID)
	if err != nil {
		return zero, err
	}
	return store.Launch(ctx, postgres.ReviewLaunch{Snapshot: snapshot, Request: request})
}
func (s *Service) PRReview(ctx context.Context, id string) (prreviews.Review, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return prreviews.Review{}, ErrAccess
	}
	result, err := postgres.NewPRReviewStore(s.pool).Get(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrAccess
	}
	return result, err
}
func (s *Service) ListPRReviews(ctx context.Context, source string, limit int, cursor string) (prreviews.Page, error) {
	result := prreviews.Page{Items: []prreviews.Review{}}
	sourceID, err := uuid.Parse(source)
	if err != nil {
		return result, ErrAccess
	}
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return result, ErrAccess
	}
	var after struct {
		Created time.Time
		ID      uuid.UUID
	}
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(data) > 200 || json.Unmarshal(data, &after) != nil || after.ID == uuid.Nil || after.Created.IsZero() {
			return result, ErrAccess
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id,created_at FROM pr_reviews WHERE source_run_id=$1 AND ($2::timestamptz IS NULL OR (created_at,id)<($2,$3)) ORDER BY created_at DESC,id DESC LIMIT $4`, sourceID, func() any {
		if cursor == "" {
			return nil
		}
		return after.Created
	}(), after.ID, limit+1)
	if err != nil {
		return result, err
	}
	type row struct {
		id      uuid.UUID
		created time.Time
	}
	items := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.created); err != nil {
			rows.Close()
			return result, err
		}
		items = append(items, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(items) > limit {
		items = items[:limit]
		after.ID = items[limit-1].id
		after.Created = items[limit-1].created
		b, _ := json.Marshal(after)
		result.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	store := postgres.NewPRReviewStore(s.pool)
	for _, item := range items {
		review, err := store.Get(ctx, item.id)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, review)
	}
	return result, nil
}

func (s *Service) ProcessPRReviewLaunch(ctx context.Context) (bool, error) {
	owner := uuid.New()
	var id, source, project uuid.UUID
	var attempts int
	err := s.pool.QueryRow(ctx, `WITH next AS(SELECT id FROM pr_review_launch_intents WHERE automatic AND status='pending' AND next_attempt_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE pr_review_launch_intents p SET lease_owner=$1,lease_until=now()+interval '2 minutes',attempts=attempts+1 FROM next WHERE p.id=next.id RETURNING p.id,p.source_run_id,p.project_id,p.attempts`, owner).Scan(&id, &source, &project, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	snapshot, err := s.prepareReview(ctx, source, uuid.Nil)
	if err == nil {
		request := prreviews.LaunchRequest{RequestKey: source, ReviewerID: snapshot.Reviewer.AgentID, ExpectedInputFingerprint: snapshot.InputFingerprint, Mode: "normal"}
		_, err = postgres.NewPRReviewStore(s.pool).Launch(ctx, postgres.ReviewLaunch{Snapshot: snapshot, Request: request, Automatic: true, IntentID: id, IntentOwner: owner})
	}
	if err == nil {
		return true, nil
	}
	// A committed launch is already status=launched; the fenced update cannot revive it.
	safeError := "Review launch is temporarily unavailable. Retry after checking the reviewer and GitHub connection."
	if reviewAvailabilityError(err) {
		safeError = err.Error()
	}
	_, saveErr := s.pool.Exec(ctx, `UPDATE pr_review_launch_intents SET lease_owner=NULL,lease_until=NULL,last_error=$3,next_attempt_at=now()+$4::interval WHERE id=$1 AND lease_owner=$2 AND status='pending'`, id, owner, strings.ReplaceAll(safeError, "\x00", ""), retryDelay(attempts).String())
	return true, saveErr
}
