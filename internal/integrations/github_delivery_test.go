package integrations_test

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

type deliveryProvider struct {
	mu                                  sync.Mutex
	changes                             git.DeliveryChanges
	branch, commit, run                 string
	pr                                  map[string]any
	refs, prs, writes                   int
	loseRef, losePR, rejectPR, hiddenPR bool
	foreignRef                          bool
	ignoreDraft                         bool
	rejectStatus                        int
	afterCommit                         func()
	t                                   *testing.T
}

func (p *deliveryProvider) serve(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/repos/fixture/private-source/") {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") == "" {
		p.t.Error("provider request lost trusted credential")
	}
	respond := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	path := strings.TrimPrefix(r.URL.Path, "/repos/fixture/private-source")
	if r.Method != "GET" {
		p.writes++
	}
	switch {
	case r.Method == "GET" && strings.HasPrefix(path, "/git/commits/"):
		respond(200, map[string]any{"sha": p.changes.BaseCommit, "tree": map[string]string{"sha": p.changes.BaseTree}})
	case r.Method == "GET" && path == "/git/ref/heads/main":
		respond(200, map[string]any{"ref": "refs/heads/main", "object": map[string]string{"sha": p.changes.BaseCommit}})
	case r.Method == "GET" && strings.HasPrefix(path, "/git/ref/heads/circular/run/"):
		if p.branch == "" && !p.foreignRef {
			respond(404, map[string]string{})
			break
		}
		sha := p.commit
		if p.foreignRef {
			sha = strings.Repeat("f", 40)
		}
		respond(200, map[string]any{"ref": "refs/heads/circular/run/" + p.run, "object": map[string]string{"sha": sha}})
	case r.Method == "POST" && path == "/git/blobs":
		var body struct{ Content, Encoding string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		data, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil || body.Encoding != "base64" {
			p.t.Error("blob encoding")
		}
		h := sha1.New()
		fmt.Fprintf(h, "blob %d%c", len(data), 0)
		h.Write(data)
		respond(201, map[string]string{"sha": hex.EncodeToString(h.Sum(nil))})
	case r.Method == "POST" && path == "/git/trees":
		var body struct {
			Base string `json:"base_tree"`
			Tree []struct {
				Path, Mode, Type string
				SHA              *string
			} `json:"tree"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Base != p.changes.BaseTree || len(body.Tree) != len(p.changes.Files) {
			p.t.Error("incorrect exact tree")
		}
		for i, file := range p.changes.Files {
			entry := body.Tree[i]
			if entry.Path != file.Path || entry.Mode != file.Mode || entry.Type != "blob" || (!file.Delete && (entry.SHA == nil || *entry.SHA != file.SHA)) || file.Delete && entry.SHA != nil {
				p.t.Errorf("wrong tree entry %+v", entry)
			}
		}
		respond(201, map[string]string{"sha": p.changes.TreeSHA})
	case r.Method == "POST" && path == "/git/commits":
		var body struct {
			Tree              string
			Parents           []string
			Author, Committer map[string]string
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Tree != p.changes.TreeSHA || len(body.Parents) != 1 || body.Parents[0] != p.changes.BaseCommit || body.Author["date"] == "" || body.Committer["date"] != body.Author["date"] {
			p.t.Error("commit changed parent or lacked stable identity")
		}
		p.commit = strings.Repeat("a", 40)
		if p.afterCommit != nil {
			p.afterCommit()
		}
		respond(201, map[string]any{"sha": p.commit, "tree": map[string]string{"sha": body.Tree}, "parents": []any{map[string]string{"sha": p.changes.BaseCommit}}})
	case r.Method == "POST" && path == "/git/refs":
		var body struct{ Ref, SHA string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Ref != "refs/heads/circular/run/"+p.run || body.SHA != p.commit {
			p.t.Error("wrong publication branch")
		}
		p.refs++
		p.branch = body.Ref
		if p.loseRef {
			p.loseRef = false
			respond(201, "lost response")
			break
		}
		respond(201, map[string]any{"ref": p.branch, "object": map[string]string{"sha": p.commit}})
	case r.Method == "GET" && path == "/pulls":
		if r.URL.Query().Get("state") != "all" || r.URL.Query().Get("head") != "fixture:circular/run/"+p.run || r.URL.Query().Get("base") != "main" {
			p.t.Error("PR lookup omitted identity")
		}
		items := []any{}
		if p.pr != nil && !p.hiddenPR {
			items = append(items, p.pr)
		}
		respond(200, items)
	case r.Method == "POST" && path == "/pulls":
		if p.rejectStatus != 0 {
			respond(p.rejectStatus, map[string]string{})
			break
		}
		if p.rejectPR {
			p.rejectPR = false
			respond(403, map[string]string{})
			break
		}
		var body struct {
			Head, Base, Body string
			Draft            bool
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !body.Draft || body.Head != "circular/run/"+p.run || body.Base != "main" || !strings.Contains(body.Body, "circular-run:"+p.run) {
			p.t.Error("unsafe PR body")
		}
		p.prs++
		p.pr = map[string]any{"user": map[string]int{"id": 101}, "number": 7, "html_url": "https://github.com/fixture/private-source/pull/7", "body": body.Body, "draft": !p.ignoreDraft, "head": map[string]any{"ref": body.Head, "sha": p.commit, "repo": map[string]int{"id": 202}}, "base": map[string]any{"ref": body.Base, "repo": map[string]int{"id": 202}}}
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_") {
			p.pr["user"] = map[string]int{"id": 501}
		}
		if p.losePR {
			p.losePR = false
			respond(201, "lost response")
			break
		}
		respond(201, p.pr)
	default:
		p.t.Errorf("unexpected GitHub mutation/path %s %s", r.Method, path)
		respond(500, map[string]string{})
	}
	return true
}

type githubDeliveryFixture struct {
	fixture
	remote                *deliveryProvider
	run, repository, root string
}

func newDeliveryFixture(t *testing.T, empty bool) githubDeliveryFixture {
	t.Helper()
	pool := testsupport.Database(t)
	provider := testsupport.NewProviderFixture()
	remote := &deliveryProvider{t: t}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !remote.serve(w, r) {
			provider.ServeHTTP(w, r)
		}
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	config := integrations.Config{ArtifactRoot: filepath.Join(root, "artifacts"), RepositoryCacheRoot: filepath.Join(root, "cache"), EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)), GitHub: integrations.OAuthApp{ClientID: "fixture-github", ClientSecret: "fixture-github-secret"}, Linear: integrations.OAuthApp{ClientID: "fixture-linear", ClientSecret: "fixture-linear-secret"}, GitHubURL: server.URL, GitHubAPIURL: server.URL, LinearURL: server.URL, LinearAPIURL: server.URL}
	service, err := integrations.New(pool, config)
	if err != nil {
		t.Fatal(err)
	}
	project := uuid.NewString()
	if _, err = pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Delivery')`, project); err != nil {
		t.Fatal(err)
	}
	f := fixture{pool: pool, service: service, provider: provider, project: project, config: config}
	f.connect(t, "github")
	f.connect(t, "linear")
	enable(t, f)
	permissions := githubConfig()
	permissions.Contents = "write"
	permissions.PullRequests = "write"
	provider.SetGitHubCreation(permissions)
	run := linearRun(t, f)
	var repo string
	if err = pool.QueryRow(t.Context(), `SELECT repository_id FROM tasks WHERE id=(SELECT task_id FROM runs WHERE id=$1)`, run).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `UPDATE repositories SET name='fixture/private-source',external_refs='{"github":{"repository_id":"202","installation_id":"101"}}' WHERE id=$1`, repo); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, source, "init", "--initial-branch=main")
	deliveryGit(t, source, "config", "user.name", "Fixture")
	deliveryGit(t, source, "config", "user.email", "fixture@example.invalid")
	if err = os.WriteFile(filepath.Join(source, "README.md"), []byte("Initial\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "obsolete.txt"), []byte("Remove this tracked file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deliveryGit(t, source, "add", ".")
	deliveryGit(t, source, "commit", "-m", "Initial")
	local, err := git.NewLocal(git.Config{RepositoryCacheRoot: config.RepositoryCacheRoot, WorktreeRoot: filepath.Join(root, "worktrees")})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := local.Checkout(t.Context(), uuid.MustParse(repo), source)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := local.Provision(t.Context(), uuid.MustParse(run), cache, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		if err = os.Remove(filepath.Join(worktree.Path, "obsolete.txt")); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(worktree.Path, "hello.txt"), []byte("Hello from Circular\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	diff, err := local.Capture(t.Context(), worktree.Path)
	if err != nil {
		t.Fatal(err)
	}
	remote.changes, err = local.PrepareDelivery(t.Context(), uuid.MustParse(repo), uuid.MustParse(run), diff.Content, "")
	if err != nil {
		t.Fatal(err)
	}
	remote.run = run
	store, err := artifacts.NewLocalStore(config.ArtifactRoot)
	if err != nil {
		t.Fatal(err)
	}
	content, err := store.Write(t.Context(), uuid.MustParse(run), "git-diff.patch", diff.Content)
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := json.Marshal(map[string]any{"size_bytes": content.SizeBytes, "sha256": content.SHA256, "changed_files": diff.ChangedFiles})
	if _, err = pool.Exec(t.Context(), `INSERT INTO artifacts(id,run_id,kind,uri,metadata) VALUES($1,$2,'diff',$3,$4)`, artifacts.DiffID(uuid.MustParse(run)), run, content.URI, metadata); err != nil {
		t.Fatal(err)
	}
	if err = local.Release(t.Context(), worktree, git.ReleaseOptions{DiscardChanges: true}); err != nil {
		t.Fatal(err)
	}
	transition(t, f, run, "succeeded")
	return githubDeliveryFixture{f, remote, run, repo, root}
}
func deliveryGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git failed: %s %v", out, err)
	}
	return strings.TrimSpace(string(out))
}
func (f githubDeliveryFixture) process(t *testing.T) {
	t.Helper()
	worked, err := f.service.ProcessGitHubRunDelivery(t.Context())
	if err != nil || !worked {
		t.Fatal(worked, err)
	}
}
func (f githubDeliveryFixture) queue(t *testing.T) {
	t.Helper()
	v, err := f.service.QueueGitHubRunDelivery(t.Context(), f.run)
	if err != nil || (v.Status != "pending" && v.Status != "retrying") {
		t.Fatal(v, err)
	}
}

func TestGitHubDeliveryPublishesRetainedRunAndLinearLinkOnce(t *testing.T) {
	f := newDeliveryFixture(t, false)
	v, err := f.service.GitHubRunDelivery(t.Context(), f.run)
	if err != nil || v.Status != "not_requested" {
		t.Fatal(v, err)
	}
	f.queue(t)
	f.process(t)
	v, err = f.service.GitHubRunDelivery(t.Context(), f.run)
	if err != nil || v.Status != "delivered" || v.Number != 7 || !v.Draft || v.BaseCommit != f.remote.changes.BaseCommit || v.BaseBranch != "main" {
		t.Fatal(v, err)
	}
	for range 3 {
		if _, err = f.service.QueueGitHubRunDelivery(t.Context(), f.run); err != nil {
			t.Fatal(err)
		}
	}
	if worked, err := f.service.ProcessGitHubRunDelivery(t.Context()); worked || err != nil {
		t.Fatal(worked, err)
	}
	if f.remote.refs != 1 || f.remote.prs != 1 {
		t.Fatal("duplicate publication", f.remote.refs, f.remote.prs)
	}
	var phase string
	if err = f.pool.QueryRow(t.Context(), `SELECT phase FROM linear_run_updates WHERE run_id=$1 AND phase='pull_request'`, f.run).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	deliver(t, f.fixture)
	deliver(t, f.fixture)
	comments := f.provider.LinearComments()
	found := false
	for _, comment := range comments {
		if strings.Contains(comment.Body, "draft pull request ready") && strings.Contains(comment.Body, v.PullRequestURL) {
			found = true
		}
	}
	if !found {
		t.Fatal("Linear missing verified PR link", comments)
	}
}

func TestGitHubDeliveryLostResponsesRecoverWithoutSecondBranchOrPR(t *testing.T) {
	for _, kind := range []string{"ref", "pull_request", "hidden_pull_request"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliveryFixture(t, false)
			f.remote.loseRef = kind == "ref"
			f.remote.losePR = kind != "ref"
			f.remote.hiddenPR = kind == "hidden_pull_request"
			f.queue(t)
			f.process(t)
			if _, err := f.pool.Exec(t.Context(), `UPDATE github_run_deliveries SET next_attempt_at=now() WHERE run_id=$1`, f.run); err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.QueueGitHubRunDelivery(t.Context(), f.run); err != nil {
				t.Fatal(err)
			}
			f.process(t)
			v, err := f.service.GitHubRunDelivery(t.Context(), f.run)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "hidden_pull_request" {
				if v.Status != "uncertain" || f.remote.prs != 1 {
					t.Fatal(v, f.remote.prs)
				}
				f.remote.hiddenPR = false
				f.queue(t)
				f.process(t)
				v, _ = f.service.GitHubRunDelivery(t.Context(), f.run)
			}
			if v.Status != "delivered" || f.remote.refs != 1 || f.remote.prs != 1 {
				t.Fatal(v, f.remote.refs, f.remote.prs)
			}
		})
	}
}

func TestGitHubDeliveryPermissionsConflictsAndTamperedArtifactsNeverOverwrite(t *testing.T) {
	for _, kind := range []string{"permission", "foreign_branch", "artifact", "no_changes", "reject_pr"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliveryFixture(t, kind == "no_changes")
			switch kind {
			case "permission":
				permissions := githubConfig()
				f.provider.SetGitHubCreation(permissions)
			case "foreign_branch":
				f.remote.foreignRef = true
			case "artifact":
				if _, err := f.pool.Exec(t.Context(), `UPDATE artifacts SET metadata=jsonb_set(metadata::jsonb,'{sha256}','"incorrect"')::json WHERE run_id=$1`, f.run); err != nil {
					t.Fatal(err)
				}
			case "reject_pr":
				f.remote.rejectPR = true
			}
			f.queue(t)
			f.process(t)
			v, err := f.service.GitHubRunDelivery(t.Context(), f.run)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "no_changes":
				if v.Status != "no_changes" {
					t.Fatal(v)
				}
			case "permission", "reject_pr":
				if v.Status != "retrying" || v.Error == "" {
					t.Fatal(v)
				}
				permissions := githubConfig()
				permissions.Contents = "write"
				permissions.PullRequests = "write"
				f.provider.SetGitHubCreation(permissions)
				if _, err = f.pool.Exec(t.Context(), `UPDATE github_run_deliveries SET next_attempt_at=now() WHERE run_id=$1`, f.run); err != nil {
					t.Fatal(err)
				}
				f.process(t)
				v, _ = f.service.GitHubRunDelivery(t.Context(), f.run)
				if v.Status != "delivered" {
					t.Fatal(v)
				}
			default:
				if v.Status != "failed" || v.Error == "" {
					t.Fatal(v)
				}
			}
			if (kind == "no_changes" || kind == "artifact") && f.remote.writes != 0 {
				t.Fatal("invalid artifact published")
			}
			if kind == "foreign_branch" && f.remote.refs+f.remote.prs != 0 {
				t.Fatal("foreign branch overwritten")
			}
		})
	}
}

func TestGitHubDeliveryConcurrentWorkersAndOptInHasNoBackfill(t *testing.T) {
	f := newDeliveryFixture(t, false)
	settings, err := f.service.SetGitHubRunDeliverySettings(t.Context(), f.project, true)
	if err != nil || !settings.Enabled || !settings.Authorized {
		t.Fatal(settings, err)
	}
	if worked, err := f.service.ProcessGitHubRunDelivery(t.Context()); err != nil || worked {
		t.Fatal("setting backfilled old run", worked, err)
	}
	f.queue(t)
	var group sync.WaitGroup
	problems := make(chan error, 6)
	for range 6 {
		group.Go(func() { _, err := f.service.ProcessGitHubRunDelivery(t.Context()); problems <- err })
	}
	group.Wait()
	close(problems)
	for err := range problems {
		if err != nil {
			t.Fatal(err)
		}
	}
	v, err := f.service.GitHubRunDelivery(t.Context(), f.run)
	if err != nil || v.Status != "delivered" || f.remote.prs != 1 {
		t.Fatal(v, err, f.remote.prs)
	}
	next := linearRun(t, f.fixture)
	transition(t, f.fixture, next, "succeeded")
	v, err = f.service.GitHubRunDelivery(t.Context(), next)
	if err != nil || v.Status != "pending" {
		t.Fatal("new success not automatically queued", v, err)
	}
	if _, err = f.service.SetGitHubRunDeliverySettings(t.Context(), f.project, false); err != nil {
		t.Fatal(err)
	}
	v, err = f.service.GitHubRunDelivery(t.Context(), next)
	if err != nil || v.Status != "failed" {
		t.Fatal(v, err)
	}
}

func TestGitHubDeliveryRejectsUnsupportedDraftsAndChangedArtifactBase(t *testing.T) {
	for _, kind := range []string{"rejected", "draft_ignored", "changed_base"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliveryFixture(t, false)
			switch kind {
			case "rejected":
				f.remote.rejectStatus = 422
			case "draft_ignored":
				f.remote.ignoreDraft = true
			case "changed_base":
				if _, err := f.pool.Exec(t.Context(), `UPDATE artifacts SET metadata=jsonb_set(metadata::jsonb,'{base_commit}',to_jsonb($2::text))::json WHERE run_id=$1`, f.run, strings.Repeat("b", 40)); err != nil {
					t.Fatal(err)
				}
			}
			f.queue(t)
			f.process(t)
			v, err := f.service.GitHubRunDelivery(t.Context(), f.run)
			if err != nil {
				t.Fatal(err)
			}
			expected := "failed"
			if kind == "draft_ignored" {
				expected = "uncertain"
			}
			if v.Status != expected || v.Error == "" {
				t.Fatal(v)
			}
			if kind == "changed_base" && f.remote.writes != 0 {
				t.Fatal("unverified base published")
			}
		})
	}
}

func TestGitHubDeliveryDisableAfterObjectsStopsBeforeBranchPublication(t *testing.T) {
	f := newDeliveryFixture(t, false)
	if _, err := f.service.SetGitHubRunDeliverySettings(t.Context(), f.project, true); err != nil {
		t.Fatal(err)
	}
	f.queue(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE github_run_deliveries SET automatic=true WHERE run_id=$1`, f.run); err != nil {
		t.Fatal(err)
	}
	f.remote.afterCommit = func() {
		if _, err := f.pool.Exec(t.Context(), `UPDATE github_run_delivery_settings SET enabled=false WHERE project_id=$1`, f.project); err != nil {
			t.Error(err)
		}
	}
	f.process(t)
	v, err := f.service.GitHubRunDelivery(t.Context(), f.run)
	if err != nil || v.Status != "failed" || f.remote.refs != 0 || f.remote.prs != 0 {
		t.Fatal(v, err)
	}
	// A deliberate manual retry resumes the same saved commit despite opt-out.
	f.queue(t)
	f.process(t)
	v, err = f.service.GitHubRunDelivery(t.Context(), f.run)
	if err != nil || v.Status != "delivered" || f.remote.refs != 1 || f.remote.prs != 1 {
		t.Fatal(v, err)
	}
}

func TestGitHubDeliveryLostPRRecoveryDoesNotRecreateDeletedBranchOrRequireArtifacts(t *testing.T) {
	f := newDeliveryFixture(t, false)
	f.remote.losePR = true
	f.queue(t)
	f.process(t)
	f.remote.branch = ""
	if err := os.RemoveAll(filepath.Join(f.root, "cache")); err != nil {
		t.Fatal(err)
	}
	f.queue(t)
	f.process(t)
	v, err := f.service.GitHubRunDelivery(t.Context(), f.run)
	if err != nil || v.Status != "delivered" || f.remote.refs != 1 || f.remote.prs != 1 || f.remote.branch != "" {
		t.Fatal(v, err, f.remote.refs, f.remote.prs)
	}
}
