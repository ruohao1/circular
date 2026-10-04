package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
)

var (
	errDeliveryContent     = errors.New("the retained diff or original Git base could not be verified; inspect the saved changes or start a new run")
	errDeliveryRejected    = errors.New("GitHub rejected the draft pull request; check repository rules and whether draft pull requests are available for this repository")
	errDeliveryUnsupported = errors.New("this run changes GitHub workflows or submodules, which Circular cannot publish; review and publish these changes separately")
	errDeliveryConflict    = errors.New("the run branch or pull request has different content on GitHub; Circular will not overwrite it")
	errDeliveryUncertain   = errors.New("GitHub may have created the pull request, but Circular could not confirm it; retry checks for the existing request without creating a duplicate")
	errDeliveryDisabled    = errors.New("automatic pull requests were turned off before publication; create this pull request manually to resume")
	deliverySHA            = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func (s *Service) publishGitHubRun(ctx context.Context, d *githubDelivery) error {
	var valid, enabled bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runs r JOIN tasks t ON t.id=r.task_id JOIN repositories repo ON repo.id=t.repository_id AND repo.project_id=t.project_id WHERE r.id=$1 AND r.status='succeeded' AND t.project_id=$2 AND repo.id=$3 AND repo.external_refs->'github'->>'repository_id'=$4 AND repo.external_refs->'github'->>'installation_id'=$5),COALESCE((SELECT enabled FROM github_run_delivery_settings WHERE project_id=$2),false)`, d.Run, d.Project, d.Repository, d.GitHubRepository, d.Installation).Scan(&valid, &enabled)
	if err != nil {
		return err
	}
	if !valid {
		return errDeliveryConflict
	}
	if d.Automatic && !enabled && !d.PRStarted {
		return errDeliveryDisabled
	}
	if !githubName.MatchString(d.Name) || !positiveNumber(d.GitHubRepository) || !positiveNumber(d.Installation) || d.Branch != "circular/run/"+d.Run || d.BaseBranch == "" || strings.ContainsAny(d.BaseBranch, "\x00\r\n") || d.BaseBranch == d.Branch {
		return errDeliveryContent
	}
	// Once a PR request was submitted, recovery is read-only. In particular,
	// never recreate a branch that a reviewer deleted after merging or closing it.
	// The saved commit receipt is sufficient even if retained files were removed.
	if d.PRStarted || d.Legacy {
		if !deliverySHA.MatchString(d.CommitSHA) || !deliverySHA.MatchString(d.BaseCommit) {
			return errDeliveryUncertain
		}
		return s.withGitHubRepository(ctx, d.Project, d.Repository, GitHubReviewRead, func(token string, actor PublisherIdentity) error {
			d.CredentialApp = actor.Mode == "app"
			if err := s.deliveryRepository(ctx, token, d); err != nil {
				return err
			}
			return s.deliverGitHubPR(ctx, token, d)
		})
	}
	changes, err := s.deliveryChanges(ctx, d)
	if err != nil {
		return err
	}
	if len(changes.Files) == 0 {
		_, err = s.pool.Exec(ctx, `UPDATE github_run_deliveries SET status='no_changes',last_error='',base_commit=$2,updated_at=now() WHERE run_id=$1`, d.Run, changes.BaseCommit)
		return err
	}
	for _, file := range changes.Files {
		if strings.HasPrefix(strings.ToLower(file.Path), ".github/workflows/") || (!file.Delete && (file.Mode != "100644" && file.Mode != "100755" && file.Mode != "120000" || !deliverySHA.MatchString(file.SHA))) {
			return errDeliveryUnsupported
		}
	}
	if d.BaseCommit == "" {
		if changes.BaseRef != "" {
			d.BaseBranch = strings.TrimPrefix(changes.BaseRef, "origin/")
		}
		if d.BaseBranch == "" || d.BaseBranch == d.Branch {
			return errDeliveryContent
		}
		_, err = s.pool.Exec(ctx, `UPDATE github_run_deliveries SET base_commit=$2,base_branch=$3,updated_at=now() WHERE run_id=$1`, d.Run, changes.BaseCommit, d.BaseBranch)
		if err != nil {
			return err
		}
		d.BaseCommit = changes.BaseCommit
	}
	if d.Publisher == nil {
		p, err := s.PublicationIdentity(ctx, d.Project, "github", d.Installation)
		if err != nil {
			return err
		}
		d.Publisher = &p
		raw, _ := json.Marshal(p)
		if _, err := s.pool.Exec(ctx, `UPDATE github_run_deliveries SET publisher=$2 WHERE run_id=$1 AND publisher IS NULL`, d.Run, raw); err != nil {
			return err
		}
	}
	return s.withGitHubPublisher(ctx, d.Project, d.Repository, GitHubPublish, *d.Publisher, false, func(token string, actor PublisherIdentity) error {
		d.CredentialApp = actor.Mode == "app"
		if !d.CredentialApp {
			if err := s.deliveryPermission(ctx, token, d.Installation); err != nil {
				return err
			}
		}
		if err := s.deliveryRepository(ctx, token, d); err != nil {
			return err
		}
		prefix := "/repos/" + d.Name
		var base struct {
			SHA  string `json:"sha"`
			Tree struct {
				SHA string `json:"sha"`
			} `json:"tree"`
		}
		status, err := s.githubRequest(ctx, token, http.MethodGet, prefix+"/git/commits/"+d.BaseCommit, nil, &base)
		if err != nil {
			return err
		}
		if status != 200 {
			return deliveryHTTPError(status)
		}
		if base.SHA != d.BaseCommit || base.Tree.SHA != changes.BaseTree {
			return errDeliveryContent
		}
		// Verify the named destination still exists. Its current SHA may advance;
		// this commit remains parented at the run's immutable original base.
		var destination gitHubRef
		status, err = s.githubRequest(ctx, token, http.MethodGet, prefix+"/git/ref/heads/"+url.PathEscape(d.BaseBranch), nil, &destination)
		if err != nil {
			return err
		}
		if status != 200 {
			return deliveryHTTPError(status)
		}
		if destination.Ref != "refs/heads/"+d.BaseBranch || !deliverySHA.MatchString(destination.Object.SHA) {
			return errDeliveryContent
		}
		if d.CommitSHA == "" {
			entries := make([]map[string]any, 0, len(changes.Files))
			for _, file := range changes.Files {
				if strings.HasPrefix(strings.ToLower(file.Path), ".github/workflows/") {
					return errDeliveryContent
				}
				item := map[string]any{"path": file.Path, "mode": file.Mode, "type": "blob", "sha": nil}
				if !file.Delete {
					if file.Mode != "100644" && file.Mode != "100755" && file.Mode != "120000" || !deliverySHA.MatchString(file.SHA) {
						return errDeliveryContent
					}
					body, _ := json.Marshal(map[string]any{"content": base64.StdEncoding.EncodeToString(file.Content), "encoding": "base64"})
					var blob struct {
						SHA string `json:"sha"`
					}
					status, err = s.githubDeliveryWrite(ctx, token, d, "github_blob", prefix+"/git/blobs", body, &blob)
					if err != nil {
						return err
					}
					if status != 201 {
						return deliveryHTTPError(status)
					}
					if blob.SHA != file.SHA {
						return errDeliveryContent
					}
					item["sha"] = blob.SHA
				}
				entries = append(entries, item)
			}
			treeBody, _ := json.Marshal(map[string]any{"base_tree": changes.BaseTree, "tree": entries})
			var tree struct {
				SHA string `json:"sha"`
			}
			status, err = s.githubDeliveryWrite(ctx, token, d, "github_tree", prefix+"/git/trees", treeBody, &tree)
			if err != nil {
				return err
			}
			if status != 201 {
				return deliveryHTTPError(status)
			}
			if tree.SHA != changes.TreeSHA {
				return errDeliveryContent
			}
			identity := map[string]string{"name": "Circular", "email": "circular@users.noreply.github.com", "date": d.Created.UTC().Format(time.RFC3339)}
			commitBody, _ := json.Marshal(map[string]any{"message": deliveryTitle(d.Title) + "\n\nCircular run: " + d.Run, "tree": tree.SHA, "parents": []string{d.BaseCommit}, "author": identity, "committer": identity})
			var commit struct {
				SHA  string `json:"sha"`
				Tree struct {
					SHA string `json:"sha"`
				} `json:"tree"`
				Parents []struct {
					SHA string `json:"sha"`
				} `json:"parents"`
			}
			status, err = s.githubDeliveryWrite(ctx, token, d, "github_commit", prefix+"/git/commits", commitBody, &commit)
			if err != nil {
				return err
			}
			if status != 201 {
				return deliveryHTTPError(status)
			}
			if !deliverySHA.MatchString(commit.SHA) || commit.Tree.SHA != tree.SHA || len(commit.Parents) != 1 || commit.Parents[0].SHA != d.BaseCommit {
				return errDeliveryContent
			}
			if _, err = s.pool.Exec(ctx, `UPDATE github_run_deliveries SET commit_sha=$2,updated_at=now() WHERE run_id=$1`, d.Run, commit.SHA); err != nil {
				return err
			}
			d.CommitSHA = commit.SHA
		}
		if !deliverySHA.MatchString(d.CommitSHA) {
			return errDeliveryContent
		}
		var branch gitHubRef
		status, err = s.githubRequest(ctx, token, http.MethodGet, prefix+"/git/ref/heads/"+url.PathEscape(d.Branch), nil, &branch)
		if err != nil {
			return err
		}
		switch status {
		case 200:
			if branch.Ref != "refs/heads/"+d.Branch || branch.Object.SHA != d.CommitSHA {
				return errDeliveryConflict
			}
		case 404:
			if err := s.checkAutomaticDelivery(ctx, d); err != nil {
				return err
			}
			body, _ := json.Marshal(map[string]string{"ref": "refs/heads/" + d.Branch, "sha": d.CommitSHA})
			status, err = s.githubDeliveryWrite(ctx, token, d, "github_branch", prefix+"/git/refs", body, &branch)
			if err != nil {
				return err
			}
			if status == 422 {
				return ErrProvider
			}
			if status != 201 {
				return deliveryHTTPError(status)
			}
			if branch.Ref != "refs/heads/"+d.Branch || branch.Object.SHA != d.CommitSHA {
				return errDeliveryConflict
			}
		default:
			return deliveryHTTPError(status)
		}
		return s.deliverGitHubPR(ctx, token, d)
	})
}

type gitHubRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

func (s *Service) deliveryChanges(ctx context.Context, d *githubDelivery) (git.DeliveryChanges, error) {
	var uri string
	var metadata []byte
	err := s.pool.QueryRow(ctx, `SELECT uri,metadata FROM artifacts WHERE run_id=$1 AND kind='diff' ORDER BY created_at LIMIT 1`, d.Run).Scan(&uri, &metadata)
	if err != nil {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	var expected struct {
		Size       *int64 `json:"size_bytes"`
		SHA        string `json:"sha256"`
		BaseCommit string `json:"base_commit"`
		BaseRef    string `json:"base_ref"`
	}
	if json.Unmarshal(metadata, &expected) != nil || expected.Size == nil || *expected.Size < 0 || *expected.Size > 32*1024*1024 {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	run, err := uuid.Parse(d.Run)
	if err != nil {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	repository, err := uuid.Parse(d.Repository)
	if err != nil {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	store, err := artifacts.NewLocalStore(s.config.ArtifactRoot)
	if err != nil {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	if err = store.Verify(ctx, run, artifacts.Content{URI: uri, SizeBytes: *expected.Size, SHA256: expected.SHA}); err != nil {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	patch, err := store.Read(ctx, run, uri)
	if err != nil {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	local, err := git.NewLocal(git.Config{RepositoryCacheRoot: s.config.RepositoryCacheRoot, WorktreeRoot: filepath.Join(filepath.Dir(s.config.RepositoryCacheRoot), "delivery-worktrees")})
	if err != nil {
		return git.DeliveryChanges{}, errDeliveryContent
	}
	baseCommit := d.BaseCommit
	if expected.BaseCommit != "" {
		if !deliverySHA.MatchString(expected.BaseCommit) || (baseCommit != "" && baseCommit != expected.BaseCommit) {
			return git.DeliveryChanges{}, errDeliveryContent
		}
		baseCommit = expected.BaseCommit
	}
	result, err := local.PrepareDelivery(ctx, repository, run, patch, baseCommit)
	if errors.Is(err, git.ErrDeliveryUnsupported) {
		return result, errDeliveryUnsupported
	}
	if err != nil {
		return result, errDeliveryContent
	}
	if expected.BaseRef != "" && result.BaseRef != "" && expected.BaseRef != result.BaseRef {
		return result, errDeliveryContent
	}
	if expected.BaseRef != "" {
		result.BaseRef = expected.BaseRef
	}
	return result, nil
}

func (s *Service) deliveryRepository(ctx context.Context, token string, d *githubDelivery) error {
	if d.CredentialApp {
		var repository githubRepository
		if err := s.github(ctx, token, "/repositories/"+d.GitHubRepository, &repository); err != nil {
			return err
		}
		if strconv.FormatInt(repository.ID, 10) != d.GitHubRepository || !strings.EqualFold(repository.FullName, d.Name) {
			return errDeliveryConflict
		}
		return nil
	}
	for page := 1; page <= 100; {
		items, err := s.githubRepositories(ctx, token, d.Installation, page)
		if err != nil {
			return err
		}
		for _, item := range items.Items {
			if item.ID == d.GitHubRepository {
				if !strings.EqualFold(item.Name, d.Name) {
					return errDeliveryConflict
				}
				return nil
			}
		}
		if items.NextPage == 0 {
			break
		}
		page = items.NextPage
	}
	return ErrDeliveryPermission
}

func deliveryTitle(title string) string {
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, title)
	title = strings.TrimSpace(title)
	r := []rune(title)
	if len(r) > 240 {
		title = string(r[:240])
	}
	if title == "" {
		return "Changes from Circular"
	}
	return title
}

type githubPullRequest struct {
	User struct {
		ID json.Number `json:"id"`
	} `json:"user"`
	Number int    `json:"number"`
	URL    string `json:"html_url"`
	Body   string `json:"body"`
	Draft  bool   `json:"draft"`
	Head   struct {
		Ref, SHA string
		Repo     struct {
			ID json.Number `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			ID json.Number `json:"id"`
		} `json:"repo"`
	} `json:"base"`
}

func (s *Service) deliverGitHubPR(ctx context.Context, token string, d *githubDelivery) error {
	prefix := "/repos/" + d.Name + "/pulls"
	marker := "<!-- circular-run:" + d.Run + " -->"
	var items []githubPullRequest
	query := url.Values{"state": {"all"}, "head": {strings.Split(d.Name, "/")[0] + ":" + d.Branch}, "base": {d.BaseBranch}, "per_page": {"100"}}
	status, err := s.githubRequest(ctx, token, http.MethodGet, prefix+"?"+query.Encode(), nil, &items)
	if err != nil {
		return err
	}
	if status != 200 {
		return deliveryHTTPError(status)
	}
	if len(items) > 0 {
		if len(items) != 1 || !s.validDeliveryPR(items[0], d, marker) {
			return errDeliveryConflict
		}
		return s.recordDeliveryPR(ctx, d, items[0])
	}
	// The durable started bit is written before POST, so loss of the response or
	// process cannot produce a second request. A retry only performs the read above.
	if d.PRStarted || d.Legacy {
		return errDeliveryUncertain
	}
	body := marker + "\n\n[Review this run in Circular](" + s.config.WebURL + "/runs/" + d.Run + ")\n\nChanges captured from the successful run. This draft is ready for human review."
	var agentName string
	if err := s.pool.QueryRow(ctx, `SELECT a.name FROM runs r JOIN agents a ON a.id=r.agent_id WHERE r.id=$1`, d.Run).Scan(&agentName); err != nil {
		return err
	}
	body += "\n\nAgent:\n\n" + cleanLinearSummary(agentName)
	var linearURL *string
	if err = s.pool.QueryRow(ctx, `SELECT t.external_refs->'linear'->>'url' FROM runs r JOIN tasks t ON t.id=r.task_id WHERE r.id=$1`, d.Run).Scan(&linearURL); err != nil {
		return err
	}
	if linearURL != nil && validLinearURL(*linearURL) {
		body += "\n\n[Linked Linear issue](" + *linearURL + ")"
	}
	payload, _ := json.Marshal(map[string]any{"title": deliveryTitle(d.Title), "body": body, "head": d.Branch, "base": d.BaseBranch, "draft": true, "maintainer_can_modify": false})
	if err := s.checkAutomaticDelivery(ctx, d); err != nil {
		return err
	}
	if err := s.reserveExternalEffect(ctx, d.Run, "github_pull_request"); err != nil {
		return err
	}
	if _, err = s.pool.Exec(ctx, `UPDATE github_run_deliveries SET pull_request_started=true,updated_at=now() WHERE run_id=$1`, d.Run); err != nil {
		return err
	}
	d.PRStarted = true
	var created githubPullRequest
	status, err = s.githubRequest(ctx, token, http.MethodPost, prefix, payload, &created)
	if err != nil {
		return errDeliveryUncertain
	}
	if status != 201 {
		if status == 401 || status == 403 || status == 404 || status == 422 {
			// These responses definitively rejected creation. Another concurrent client
			// may already have opened the branch PR; the next read still runs first.
			if _, err = s.pool.Exec(ctx, `UPDATE github_run_deliveries SET pull_request_started=false,updated_at=now() WHERE run_id=$1`, d.Run); err != nil {
				return err
			}
			if status == 422 {
				return errDeliveryRejected
			}
			return deliveryHTTPError(status)
		}
		return errDeliveryUncertain
	}
	if !created.Draft || !s.validDeliveryPR(created, d, marker) {
		return errDeliveryUncertain
	}
	return s.recordDeliveryPR(ctx, d, created)
}

func (s *Service) validDeliveryPR(pr githubPullRequest, d *githubDelivery, marker string) bool {
	return positiveNumber(string(pr.User.ID)) && (d.Publisher == nil || string(pr.User.ID) == d.Publisher.ActorID) && pr.Number > 0 && pr.URL == "https://github.com/"+d.Name+"/pull/"+strconv.Itoa(pr.Number) && strings.Contains(pr.Body, marker) && pr.Head.Ref == d.Branch && pr.Head.SHA == d.CommitSHA && string(pr.Head.Repo.ID) == d.GitHubRepository && pr.Base.Ref == d.BaseBranch && string(pr.Base.Repo.ID) == d.GitHubRepository
}

func (s *Service) recordDeliveryPR(ctx context.Context, d *githubDelivery, pr githubPullRequest) error {
	if d.Publisher == nil {
		d.Publisher = &PublisherIdentity{Mode: "user", Provider: "github", AccountID: d.Installation, ActorID: string(pr.User.ID)}
	}
	publisher, _ := json.Marshal(d.Publisher)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(ctx, tx)
	effectsAllowed, err := postgres.ExternalEffectAllowed(ctx, tx, uuid.MustParse(d.Run))
	if err != nil {
		return err
	}
	var automatic bool
	err = tx.QueryRow(ctx, `SELECT automatic FROM pr_review_settings WHERE project_id=$1 FOR UPDATE`, d.Project).Scan(&automatic)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var previous string
	if err = tx.QueryRow(ctx, `SELECT status FROM github_run_deliveries WHERE run_id=$1 FOR UPDATE`, d.Run).Scan(&previous); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE github_run_deliveries SET status='delivered',pull_request_url=$2,pull_request_number=$3,publisher=COALESCE(publisher,$4::jsonb),last_error='',updated_at=now() WHERE run_id=$1`, d.Run, pr.URL, pr.Number, publisher); err != nil {
		return err
	}
	if automatic && effectsAllowed && previous != "delivered" {
		parameters := json.RawMessage(`{"mode":"normal"}`)
		digest, err := prreviews.Fingerprint(parameters)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO pr_review_launch_intents(id,project_id,source_run_id,request_key,parameters,parameters_sha256,automatic) VALUES($1,$2,$3,$3,$4,$5,true) ON CONFLICT DO NOTHING`, uuid.New(), d.Project, d.Run, parameters, digest); err != nil {
			return err
		}
	}
	// Enqueue exactly one comment using the existing Linear delivery identity and
	// workspace guard. Publication itself never changes the issue's workflow state.
	_, err = tx.Exec(ctx, `INSERT INTO linear_run_updates(project_id,run_id,phase,outcome,account_id,issue_id,issue_url,summary)
 SELECT t.project_id,r.id,'pull_request','succeeded',i.account_id,(t.external_refs->'linear'->>'issue_id')::uuid,t.external_refs->'linear'->>'url',$2
 FROM runs r JOIN tasks t ON t.id=r.task_id JOIN linear_run_update_settings settings ON settings.project_id=t.project_id AND settings.enabled
 JOIN linear_connection_state i ON i.project_id=t.project_id AND i.enabled
 WHERE r.id=$1 AND NOT EXISTS(SELECT 1 FROM external_session_runs WHERE run_id=r.id) AND COALESCE(i.account_id,'')<>'' AND t.external_refs->'linear'->>'issue_id' ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
 AND t.external_refs->'linear'->>'url' LIKE 'https://linear.app/%'
 AND (t.external_refs->'linear'->>'account_id'=i.account_id OR (t.external_refs->'linear'->>'account_id' IS NULL AND left(t.external_refs->'linear'->>'url',length(i.account_url)+1)=i.account_url||'/'))
 ON CONFLICT(run_id,phase) DO NOTHING`, d.Run, pr.URL)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) checkAutomaticDelivery(ctx context.Context, d *githubDelivery) error {
	if !d.Automatic {
		return nil
	}
	var enabled bool
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE((SELECT enabled FROM github_run_delivery_settings WHERE project_id=$1),false)`, d.Project).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return errDeliveryDisabled
	}
	return nil
}

func (s *Service) githubDeliveryWrite(ctx context.Context, token string, d *githubDelivery, effect, path string, body []byte, output any) (int, error) {
	if err := s.reserveExternalEffect(ctx, d.Run, effect); err != nil {
		return 0, err
	}
	return s.githubRequest(ctx, token, http.MethodPost, path, body, output)
}
