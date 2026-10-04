package integrations_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/integrations"
	"github.com/ruohao1/circular/internal/testsupport"
)

func linearRun(t *testing.T, f fixture) string {
	t.Helper()
	repository, agent, task, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var existingTask, existingAgent string
	if err := f.pool.QueryRow(t.Context(), `SELECT r.task_id,r.agent_id FROM runs r JOIN tasks t ON t.id=r.task_id WHERE t.project_id=$1 AND t.external_refs->'linear'->>'issue_id'=$2 LIMIT 1`, f.project, testsupport.ProviderIssueID).Scan(&existingTask, &existingAgent); err == nil {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) SELECT $1,$2,$3,'fake','queued',COALESCE(max(attempt),0)+1,'{}' FROM runs WHERE task_id=$2`, run, existingTask, existingAgent); err != nil {
			t.Fatal(err)
		}
		return run
	}

	refs, _ := json.Marshal(map[string]any{"linear": map[string]string{"issue_id": testsupport.ProviderIssueID, "url": "https://linear.app/circular-fixture/issue/TST-1/imported-task", "account_id": "20000000-0000-4000-8000-000000000004"}})
	_, err := f.pool.Exec(t.Context(), `WITH r AS (INSERT INTO repositories(id,project_id,name,clone_url,default_branch,external_refs) VALUES($1::uuid,$2,$1::text,'https://github.com/fixture/private-source.git','main','{}')),
 a AS (INSERT INTO agents(id,project_id,name,backend,instructions,backend_config,enabled) VALUES($3::uuid,$2,$3::text,'fake','Fixture','{}',true)),
 t AS (INSERT INTO tasks(id,project_id,repository_id,title,description,status,external_refs) VALUES($4,$2,$1,'Task','Fixture','open',$6))
 INSERT INTO runs(id,task_id,agent_id,backend,status,attempt,external_refs) VALUES($5,$4,$3,'fake','queued',1,'{}')`, repository, f.project, agent, task, run, refs)
	if err != nil {
		t.Fatal(err)
	}
	return run
}
func transition(t *testing.T, f fixture, run, status string) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE runs SET status=$2 WHERE id=$1`, run, status); err != nil {
		t.Fatal(err)
	}
}
func deliver(t *testing.T, f fixture) {
	t.Helper()
	worked, err := f.service.ProcessLinearUpdate(t.Context())
	if err != nil || !worked {
		t.Fatalf("deliver worked=%v: %v", worked, err)
	}
}
func due(t *testing.T, f fixture) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), `UPDATE linear_run_updates SET next_attempt_at=now() WHERE project_id=$1 AND status='pending'`, f.project); err != nil {
		t.Fatal(err)
	}
}
func enable(t *testing.T, f fixture) {
	t.Helper()
	if _, err := f.service.SetLinearRunUpdates(t.Context(), f.project, true); err != nil {
		t.Fatal(err)
	}
}
func summary(t *testing.T, f fixture, run, text string) {
	t.Helper()
	data, _ := json.Marshal(map[string]string{"content": text})
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO events(id,run_id,sequence,type,source,data,occurred_at) VALUES($1,$2,1,'agent.message.completed','fake-container-workload',$3,now())`, uuid.NewString(), run, data); err != nil {
		t.Fatal(err)
	}
}

func TestLinearUpdatesRequireExplicitGrantAndOptInWithoutBackfill(t *testing.T) {
	f := setup(t)
	f.provider.SetLinearScopes("read")
	f.connect(t, "linear")
	initial, err := f.service.LinearRunUpdates(t.Context(), f.project)
	if err != nil || initial.Enabled || initial.Authorized || initial.PendingCount != 0 {
		t.Fatal(initial, err)
	}
	if _, err := f.service.SetLinearRunUpdates(t.Context(), f.project, true); !errors.Is(err, integrations.ErrReconnect) {
		t.Fatal("read-only grant enabled comments", err)
	}
	old := linearRun(t, f)
	transition(t, f, old, "succeeded")
	f.provider.SetLinearScopes("read comments:create")
	f.connect(t, "linear")
	enable(t, f)
	if worked, err := f.service.ProcessLinearUpdate(t.Context()); err != nil || worked {
		t.Fatal("old history was queued", worked, err)
	}
	state, err := f.service.RunLinearDelivery(t.Context(), old)
	if err != nil || state.Status != "skipped" {
		t.Fatal("old history status", state, err)
	}
	fresh := linearRun(t, f)
	transition(t, f, fresh, "running")
	deliver(t, f)
	if len(f.provider.LinearComments()) != 1 {
		t.Fatal("future transition not delivered")
	}
	other := uuid.NewString()
	if _, err := f.pool.Exec(t.Context(), `INSERT INTO projects(id,name) VALUES($1,'Isolated')`, other); err != nil {
		t.Fatal(err)
	}
	status, err := f.service.LinearRunUpdates(t.Context(), other)
	if err != nil || status.Enabled || status.Authorized || status.PendingCount != 0 {
		t.Fatal("settings leaked projects", status, err)
	}
}

func TestLinearUpdatesPublishProgressAndEachOutcomeWithBoundedSummary(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f := setup(t)
			f.connect(t, "linear")
			enable(t, f)
			run := linearRun(t, f)
			transition(t, f, run, "running")
			deliver(t, f)
			summary(t, f, run, "Fixed parser. https://github.com/fixture/source/pull/12\nhttps://github.com.evil.test/a/b/pull/13\n@everyone token: harmless\n```diff\n+private code\n```\nsecret=fixture-secret-value\n"+strings.Repeat("Results ", 900))
			transition(t, f, run, outcome)
			deliver(t, f)
			state, err := f.service.RunLinearDelivery(t.Context(), run)
			if err != nil || state.Status != "delivered" || state.LastDeliveredAt == nil {
				t.Fatal(state, err)
			}
			comments := f.provider.LinearComments()
			if len(comments) != 2 {
				t.Fatal("missing progress/result", comments)
			}
			var result string
			for _, comment := range comments {
				if strings.Contains(comment.Body, "Agent summary:") {
					result = comment.Body
				}
			}
			if !strings.Contains(result, "/runs/"+run) || !strings.Contains(result, "Pull requests reported by the agent:\n- https://github.com/fixture/source/pull/12") || len([]rune(result)) > 2900 {
				t.Fatal("invalid bounded result", len([]rune(result)))
			}
			if strings.Contains(result, "private code") || strings.Contains(result, "fixture-secret-value") || strings.Contains(result, "@everyone") {
				t.Fatal("unsafe excerpt", result)
			}
			if strings.Contains(result, "- https://github.com.evil") {
				t.Fatal("untrusted pull request included")
			}
			if worked, err := f.service.ProcessLinearUpdate(t.Context()); err != nil || worked {
				t.Fatal("completed delivery replayed", err)
			}
		})
	}
}

func TestLinearUpdatesRecoverLostResponseAfterRestartWithoutDuplicates(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	enable(t, f)
	run := linearRun(t, f)
	transition(t, f, run, "succeeded")
	f.provider.LoseCommentResponse.Store(true)
	deliver(t, f)
	state, err := f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || state.Status != "retrying" {
		t.Fatal(state, err)
	}
	if f.provider.CommentCreates.Load() != 1 {
		t.Fatal("provider did not accept first attempt")
	}
	server := httptest.NewServer(f.provider)
	defer server.Close()
	restarted, err := integrations.New(f.pool, integrations.Config{EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32)), Linear: integrations.OAuthApp{ClientID: "fixture-linear", ClientSecret: "fixture-linear-secret"}, LinearURL: server.URL, LinearAPIURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	f.service = restarted
	due(t, f)
	var wg sync.WaitGroup
	problems := make(chan error, 8)
	for range 8 {
		wg.Go(func() { _, err := f.service.ProcessLinearUpdate(t.Context()); problems <- err })
	}
	wg.Wait()
	close(problems)
	for err := range problems {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err = f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || state.Status != "delivered" || f.provider.CommentCreates.Load() != 1 {
		t.Fatal("duplicate or lost result", state, err)
	}
}

func TestLinearUpdatesSuppressDelayedProgressAndSurviveProviderOutage(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	enable(t, f)
	run := linearRun(t, f)
	transition(t, f, run, "running")
	f.provider.Unavailable.Store(true)
	deliver(t, f)
	transition(t, f, run, "failed")
	deliver(t, f) // obsolete progress becomes skipped
	deliver(t, f) // terminal retained while provider is down
	f.provider.Unavailable.Store(false)
	due(t, f)
	deliver(t, f)
	comments := f.provider.LinearComments()
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "run failed") {
		t.Fatal("delayed progress overtook result", comments)
	}
	state, err := f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || state.Status != "delivered" {
		t.Fatal(state, err)
	}
}

func TestLinearUpdatesDisableDisconnectAndWorkspaceChangeSuppressPending(t *testing.T) {
	for _, action := range []string{"disable", "disconnect", "workspace"} {
		t.Run(action, func(t *testing.T) {
			f := setup(t)
			f.connect(t, "linear")
			enable(t, f)
			run := linearRun(t, f)
			transition(t, f, run, "succeeded")
			switch action {
			case "disable":
				if _, err := f.service.SetLinearRunUpdates(t.Context(), f.project, false); err != nil {
					t.Fatal(err)
				}
				enable(t, f)
			case "disconnect":
				if _, err := f.service.Disconnect(t.Context(), f.project, "linear"); err != nil {
					t.Fatal(err)
				}
				f.connect(t, "linear")
			case "workspace":
				f.provider.SetLinearOrganization(uuid.NewString())
				f.connect(t, "linear")
				deliver(t, f)
			}
			if worked, err := f.service.ProcessLinearUpdate(t.Context()); err != nil || worked {
				t.Fatal("suppressed update became deliverable", err)
			}
			state, err := f.service.RunLinearDelivery(t.Context(), run)
			if err != nil || state.Status != "skipped" || len(f.provider.LinearComments()) != 0 {
				t.Fatal(state, err)
			}
		})
	}
}

func TestLinearUpdatesDeniedPermissionVisibleAndReconnectRecovers(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	enable(t, f)
	run := linearRun(t, f)
	transition(t, f, run, "succeeded")
	f.provider.RejectComments.Store(true)
	deliver(t, f)
	state, err := f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || state.Status != "failed" {
		t.Fatal(state, err)
	}
	settings, err := f.service.LinearRunUpdates(t.Context(), f.project)
	if err != nil || settings.FailedCount != 1 || settings.LastError == "" {
		t.Fatal(settings, err)
	}
	f.provider.RejectComments.Store(false)
	f.connect(t, "linear")
	deliver(t, f)
	state, err = f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || state.Status != "delivered" {
		t.Fatal(state, err)
	}
}

func TestLinearUpdatesRefreshPreservesGrantedScope(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	enable(t, f)
	var id string
	var encrypted []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT id,credentials FROM integrations WHERE project_id=$1 AND provider='linear'`, f.project).Scan(&id, &encrypted); err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(bytes.Repeat([]byte{17}, 32))
	vault, _ := cipher.NewGCM(block)
	plain, err := vault.Open(nil, encrypted[:vault.NonceSize()], encrypted[vault.NonceSize():], []byte(id))
	if err != nil {
		t.Fatal(err)
	}
	var token map[string]any
	if err := json.Unmarshal(plain, &token); err != nil {
		t.Fatal(err)
	}
	token["expires_at"] = time.Now().Add(-time.Minute)
	plain, _ = json.Marshal(token)
	nonce := make([]byte, vault.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	encrypted = vault.Seal(nonce, nonce, plain, []byte(id))
	if _, err := f.pool.Exec(t.Context(), `UPDATE integrations SET credentials=$2 WHERE id=$1`, id, encrypted); err != nil {
		t.Fatal(err)
	}
	f.provider.OmitRefreshScope.Store(true)
	f.provider.BeforeLinearComment = func() {
		var committed []byte
		if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM integrations WHERE id=$1`, id).Scan(&committed); err != nil {
			t.Error(err)
			return
		}
		if bytes.Equal(committed, encrypted) {
			t.Error("comment attempted before refreshed grant was committed")
		}
	}
	run := linearRun(t, f)
	transition(t, f, run, "succeeded")
	deliver(t, f)
	if f.provider.CommentCreates.Load() != 1 {
		t.Fatal("comment did not follow the committed refresh")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.service.ProcessLinearUpdate(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	state, err := f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || state.Status != "delivered" {
		t.Fatal("committed refresh was lost across cancellation", state, err)
	}
	settings, err := f.service.LinearRunUpdates(t.Context(), f.project)
	if err != nil || !settings.Authorized || f.provider.Refreshes.Load() != 1 {
		t.Fatal("refresh lost grant", settings, err)
	}
}

func TestLinearUpdatesLoopStopsOnCancellation(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.service.RunLinearUpdates(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLinearUpdatesUnreadableCredentialsRequireReconnectAndArePreserved(t *testing.T) {
	f := setup(t)
	f.connect(t, "linear")
	enable(t, f)
	run := linearRun(t, f)
	transition(t, f, run, "succeeded")
	broken := []byte("unreadable encrypted credentials")
	if _, err := f.pool.Exec(t.Context(), `UPDATE integrations SET credentials=$2 WHERE project_id=$1 AND provider='linear'`, f.project, broken); err != nil {
		t.Fatal(err)
	}
	deliver(t, f)
	state, err := f.service.RunLinearDelivery(t.Context(), run)
	if err != nil || state.Status != "reconnect_required" {
		t.Fatal(state, err)
	}
	settings, err := f.service.LinearRunUpdates(t.Context(), f.project)
	if err != nil || settings.Authorized {
		t.Fatal(settings, err)
	}
	var stored []byte
	if err := f.pool.QueryRow(t.Context(), `SELECT credentials FROM integrations WHERE project_id=$1 AND provider='linear'`, f.project).Scan(&stored); err != nil || !bytes.Equal(stored, broken) {
		t.Fatal("unreadable credential was discarded", err)
	}
}
