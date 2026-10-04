package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/postgres"
)

var ErrDeliveryNotReady = errors.New("only a successful run with retained changes and an attached GitHub repository can create a pull request")
var ErrDeliveryPermission = errors.New("allow Repository Contents and Pull requests: read and write in the GitHub App, approve the installation update, then retry")

type GitHubDeliverySettings struct {
	Enabled           bool   `json:"enabled"`
	Authorized        bool   `json:"authorized"`
	PermissionMessage string `json:"permission_message"`
	PendingCount      int    `json:"pending_count"`
	FailedCount       int    `json:"failed_count"`
	LastError         string `json:"last_error"`
}

type GitHubDelivery struct {
	Publisher      json.RawMessage `json:"publisher,omitempty"`
	Status         string          `json:"status"`
	PullRequestURL string          `json:"pull_request_url"`
	Number         int             `json:"number"`
	Branch         string          `json:"branch"`
	BaseBranch     string          `json:"base_branch"`
	BaseCommit     string          `json:"base_commit"`
	Draft          bool            `json:"draft"`
	Error          string          `json:"error"`
	Retryable      bool            `json:"retryable"`
}

type githubDelivery struct {
	Run, Project, Repository, GitHubRepository, Installation, Name, BaseBranch, Branch, Title string
	Status, BaseCommit, CommitSHA, URL, Error                                                 string
	Automatic, PRStarted                                                                      bool
	Number, Attempts                                                                          int
	Created                                                                                   time.Time
	Publisher                                                                                 *PublisherIdentity
	Legacy, CredentialApp                                                                     bool
}

func (s *Service) GitHubRunDeliverySettings(ctx context.Context, project string) (GitHubDeliverySettings, error) {
	var v GitHubDeliverySettings
	err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT enabled FROM github_run_delivery_settings WHERE project_id=$1),false),
 (SELECT count(*) FROM github_run_deliveries WHERE project_id=$1 AND status='pending'),
 (SELECT count(*) FROM github_run_deliveries WHERE project_id=$1 AND status IN ('failed','uncertain')),
 COALESCE((SELECT last_error FROM github_run_deliveries WHERE project_id=$1 AND status IN ('pending','failed','uncertain') AND last_error<>'' ORDER BY updated_at DESC LIMIT 1),'') FROM projects WHERE id=$1`, project).Scan(&v.Enabled, &v.PendingCount, &v.FailedCount, &v.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrAccess
	}
	if err != nil {
		return v, err
	}
	err = s.githubProjectPermission(ctx, project)
	if err == nil {
		v.Authorized = true
	} else if errors.Is(err, ErrDeliveryPermission) || errors.Is(err, ErrReconnect) || errors.Is(err, ErrConfiguration) || errors.Is(err, ErrAccess) || errors.Is(err, ErrProvider) || errors.Is(err, ErrIdentityUnavailable) {
		v.PermissionMessage = err.Error()
	} else {
		return v, err
	}
	return v, nil
}

func (s *Service) SetGitHubRunDeliverySettings(ctx context.Context, project string, enabled bool) (GitHubDeliverySettings, error) {
	if enabled {
		settings, err := s.GitHubRunDeliverySettings(ctx, project)
		if err != nil {
			return settings, err
		}
		if !settings.Authorized {
			return settings, ErrDeliveryPermission
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return GitHubDeliverySettings{}, err
	}
	defer rollback(ctx, tx)
	if _, err = tx.Exec(ctx, `INSERT INTO github_run_delivery_settings(project_id,enabled) VALUES($1,$2) ON CONFLICT(project_id) DO UPDATE SET enabled=EXCLUDED.enabled,updated_at=now()`, project, enabled); err != nil {
		return GitHubDeliverySettings{}, err
	}
	if !enabled {
		if _, err = tx.Exec(ctx, `UPDATE github_run_deliveries SET status='failed',last_error='Automatic pull requests were turned off before publication',updated_at=now() WHERE project_id=$1 AND automatic AND status='pending' AND commit_sha='' AND NOT pull_request_started`, project); err != nil {
			return GitHubDeliverySettings{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return GitHubDeliverySettings{}, err
	}
	return s.GitHubRunDeliverySettings(ctx, project)
}

func (s *Service) GitHubRunDelivery(ctx context.Context, run string) (GitHubDelivery, error) {
	v := GitHubDelivery{Status: "not_ready", Draft: true}
	var status, kind string
	var repository *string
	var githubID, installation *string
	var changes bool
	err := s.pool.QueryRow(ctx, `SELECT r.status,r.kind,repo.id::text,repo.external_refs->'github'->>'repository_id',repo.external_refs->'github'->>'installation_id',EXISTS(SELECT 1 FROM artifacts WHERE run_id=r.id AND kind='diff') FROM runs r JOIN tasks t ON t.id=r.task_id LEFT JOIN repositories repo ON repo.id=t.repository_id AND repo.project_id=t.project_id WHERE r.id=$1`, run).Scan(&status, &kind, &repository, &githubID, &installation, &changes)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrAccess
	}
	if err != nil {
		return v, err
	}
	if kind != "coding" {
		v.Error = "PR reviews do not create pull requests."
		return v, nil
	}
	var attempts int
	err = s.pool.QueryRow(ctx, `SELECT status,pull_request_url,pull_request_number,branch,base_branch,base_commit,last_error,attempts,publisher FROM github_run_deliveries WHERE run_id=$1`, run).Scan(&v.Status, &v.PullRequestURL, &v.Number, &v.Branch, &v.BaseBranch, &v.BaseCommit, &v.Error, &attempts, &v.Publisher)
	if err == nil {
		v.Retryable = v.Status == "failed" || v.Status == "uncertain" || (v.Status == "pending" && attempts > 0)
		if v.Status == "pending" && attempts > 0 {
			v.Status = "retrying"
		}
		return v, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return v, err
	}
	switch {
	case status != "succeeded":
		v.Error = "A pull request is available after the run succeeds."
	case repository == nil || githubID == nil || installation == nil || !positiveNumber(*githubID) || !positiveNumber(*installation):
		v.Error = "Attach a GitHub repository to this task before running it."
	case !changes:
		v.Error = "This run has no retained diff to publish."
	default:
		v.Status = "not_requested"
		v.Retryable = true
	}
	return v, nil
}

// One run owns one delivery identity. Repeated requests only resume that identity.
func (s *Service) QueueGitHubRunDelivery(ctx context.Context, run string) (GitHubDelivery, error) {
	v, err := s.GitHubRunDelivery(ctx, run)
	if err != nil {
		return v, err
	}
	if v.Status == "not_ready" {
		return v, ErrDeliveryNotReady
	}
	if v.Status == "delivered" || v.Status == "no_changes" {
		return v, nil
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO github_run_deliveries(run_id,project_id,repository_id,github_repository_id,installation_id,repository_name,base_branch,branch,title)
 SELECT r.id,t.project_id,repo.id,repo.external_refs->'github'->>'repository_id',repo.external_refs->'github'->>'installation_id',repo.name,repo.default_branch,'circular/run/'||r.id::text,t.title
 FROM runs r JOIN tasks t ON t.id=r.task_id JOIN repositories repo ON repo.id=t.repository_id AND repo.project_id=t.project_id WHERE r.id=$1 AND r.status='succeeded' AND r.kind='coding'
 ON CONFLICT(run_id) DO UPDATE SET status='pending',automatic=false,next_attempt_at=now(),updated_at=now() WHERE github_run_deliveries.status IN ('pending','failed','uncertain')`, run)
	if err != nil {
		return v, err
	}
	return s.GitHubRunDelivery(ctx, run)
}

func (s *Service) deliveryPermission(ctx context.Context, token, installation string) error {
	for page := 1; page <= 100; page++ {
		var response struct {
			Total int `json:"total_count"`
			Items []struct {
				ID          json.Number       `json:"id"`
				Permissions map[string]string `json:"permissions"`
				Suspended   *string           `json:"suspended_at"`
			} `json:"installations"`
		}
		status, err := s.githubRequest(ctx, token, http.MethodGet, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), nil, &response)
		if err != nil {
			return err
		}
		if status != 200 {
			return deliveryHTTPError(status)
		}
		for _, item := range response.Items {
			if installation != "" && string(item.ID) != installation {
				continue
			}
			if item.Suspended == nil && item.Permissions["contents"] == "write" && item.Permissions["pull_requests"] == "write" {
				return nil
			}
			if installation != "" {
				return ErrDeliveryPermission
			}
		}
		if page*100 >= response.Total {
			break
		}
	}
	return ErrDeliveryPermission
}

func deliveryHTTPError(status int) error {
	switch status {
	case 401:
		return ErrReconnect
	case 403, 404:
		return ErrDeliveryPermission
	default:
		return ErrProvider
	}
}

// ProcessGitHubRunDelivery claims one run through a session advisory lock. Each
// remote receipt is committed separately before the next externally visible step.
func (s *Service) ProcessGitHubRunDelivery(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var run string
	err := s.pool.QueryRow(ctx, `SELECT run_id FROM github_run_deliveries WHERE status='pending' AND next_attempt_at<=now() ORDER BY next_attempt_at,created_at LIMIT 1`).Scan(&run)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	connection, err := s.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer connection.Release()
	var locked bool
	if err = connection.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended('github-run:'||$1,0))`, run).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err := connection.Exec(cleanup, `SELECT pg_advisory_unlock(hashtextextended('github-run:'||$1,0))`, run)
		if err != nil {
			_ = connection.Conn().Close(cleanup)
		}
	}()
	var d githubDelivery
	var publisher []byte
	err = connection.QueryRow(ctx, `SELECT run_id,project_id,repository_id,github_repository_id,installation_id,repository_name,base_branch,branch,title,automatic,status,base_commit,commit_sha,pull_request_started,pull_request_url,pull_request_number,attempts,last_error,created_at,publisher,legacy_reconcile FROM github_run_deliveries WHERE run_id=$1 AND status='pending' AND next_attempt_at<=now()`, run).Scan(&d.Run, &d.Project, &d.Repository, &d.GitHubRepository, &d.Installation, &d.Name, &d.BaseBranch, &d.Branch, &d.Title, &d.Automatic, &d.Status, &d.BaseCommit, &d.CommitSHA, &d.PRStarted, &d.URL, &d.Number, &d.Attempts, &d.Error, &d.Created, &publisher, &d.Legacy)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(publisher) > 0 && (json.Unmarshal(publisher, &d.Publisher) != nil || d.Publisher == nil || !d.Publisher.valid("github")) {
		return true, ErrIdentityConflict
	}
	err = s.publishGitHubRun(ctx, &d)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return false, err
		}
		message := ErrProvider.Error()
		status := "pending"
		switch {
		case errors.Is(err, postgres.ErrExternalStopped):
			status = "failed"
			message = err.Error()
		case errors.Is(err, ErrIdentityUnavailable), errors.Is(err, ErrIdentityConflict):
			message = err.Error()
		case errors.Is(err, ErrReconnect):
			message = ErrReconnect.Error()
		case errors.Is(err, ErrDeliveryPermission):
			message = ErrDeliveryPermission.Error()
		case errors.Is(err, errDeliveryRejected):
			status = "failed"
			message = errDeliveryRejected.Error()
		case errors.Is(err, errDeliveryUnsupported):
			status = "failed"
			message = errDeliveryUnsupported.Error()
		case errors.Is(err, errDeliveryContent):
			status = "failed"
			message = errDeliveryContent.Error()
		case errors.Is(err, errDeliveryConflict):
			status = "failed"
			message = errDeliveryConflict.Error()
		case errors.Is(err, errDeliveryUncertain):
			status = "uncertain"
			message = errDeliveryUncertain.Error()
		case errors.Is(err, errDeliveryDisabled):
			status = "failed"
			message = errDeliveryDisabled.Error()
		}
		if _, saveErr := s.pool.Exec(ctx, `UPDATE github_run_deliveries SET status=$2,last_error=$3,attempts=LEAST(attempts+1,1000000),next_attempt_at=now()+$4::interval,updated_at=now() WHERE run_id=$1`, d.Run, status, message, fmt.Sprintf("%f seconds", retryDelay(d.Attempts).Seconds())); saveErr != nil {
			return false, saveErr
		}
	}
	return true, nil
}
