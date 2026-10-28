package integrations_test

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/integrations"
)

func approvalService(t *testing.T, f fixture, ids []string) *integrations.Service {
	t.Helper()
	c := f.config
	c.ApproverGitHubIDs = ids
	s, e := integrations.New(f.pool, c)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func approvalStart(t *testing.T, s *integrations.Service) (integrations.Authorization, string) {
	t.Helper()
	a, e := s.BeginApprovalLogin(t.Context(), "/proposals/00000000-0000-4000-8000-000000000001?revision=2")
	if e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(a.URL)
	if e != nil || !strings.HasSuffix(u.Query().Get("redirect_uri"), "/api/v1/approval-auth/github/callback") || u.Query().Get("prompt") != "select_account" {
		t.Fatal(a.URL, e)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, e := c.Get(a.URL)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	u, e = url.Parse(r.Header.Get("Location"))
	if e != nil {
		t.Fatal(e)
	}
	return a, u.Query().Get("code")
}
func TestApprovalLoginSeparateFromIntegrationAndReplayBound(t *testing.T) {
	f := setup(t)
	f.connect(t, "github")
	var before string
	if e := f.pool.QueryRow(t.Context(), `SELECT to_jsonb(i)::text FROM integrations i WHERE project_id=$1 AND provider='github'`, f.project).Scan(&before); e != nil {
		t.Fatal(e)
	}
	s := approvalService(t, f, []string{"101"})
	a, code := approvalStart(t, s)
	if _, _, _, e := s.CompleteApprovalLogin(t.Context(), a.State, "wrong", code); !errors.Is(e, integrations.ErrCallback) {
		t.Fatal(e)
	}
	token, user, path, e := s.CompleteApprovalLogin(t.Context(), a.State, a.Browser, code)
	if e != nil || user.UserID != "101" || token == "" || path != "/proposals/00000000-0000-4000-8000-000000000001?revision=2" {
		t.Fatal(user, path, e)
	}
	if _, _, _, e = s.CompleteApprovalLogin(t.Context(), a.State, a.Browser, code); !errors.Is(e, integrations.ErrCallback) {
		t.Fatal("replay accepted", e)
	}
	if _, e = s.ApprovalSession(t.Context(), token); e != nil {
		t.Fatal(e)
	}
	var after string
	if e = f.pool.QueryRow(t.Context(), `SELECT to_jsonb(i)::text FROM integrations i WHERE project_id=$1 AND provider='github'`, f.project).Scan(&after); e != nil || before != after {
		t.Fatal("integration grant changed", e)
	}
	if e = s.EndApprovalSession(t.Context(), token); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApprovalSession(t.Context(), token); !errors.Is(e, integrations.ErrApprovalSession) {
		t.Fatal("logged-out session accepted", e)
	}
}
func TestApprovalLoginAllowlistExpiryAndSafeReturn(t *testing.T) {
	f := setup(t)
	s := approvalService(t, f, []string{"202"})
	a, code := approvalStart(t, s)
	if _, _, _, e := s.CompleteApprovalLogin(t.Context(), a.State, a.Browser, code); !errors.Is(e, integrations.ErrAccess) {
		t.Fatal("unlisted account allowed", e)
	}
	s = approvalService(t, f, []string{"101"})
	for _, path := range []string{"https://evil.invalid", "//evil.invalid", "/proposals/../setup", "/proposals?redirect=https://evil.invalid", "/proposals/%2f%2fevil.invalid"} {
		if _, e := s.BeginApprovalLogin(t.Context(), path); !errors.Is(e, integrations.ErrCallback) {
			t.Fatalf("unsafe return %q: %v", path, e)
		}
	}
	a, code = approvalStart(t, s)
	if _, e := f.pool.Exec(t.Context(), `UPDATE approval_oauth_states SET expires_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	if _, _, _, e := s.CompleteApprovalLogin(t.Context(), a.State, a.Browser, code); !errors.Is(e, integrations.ErrCallback) {
		t.Fatal("expired state accepted", e)
	}
	a, code = approvalStart(t, s)
	token, _, _, e := s.CompleteApprovalLogin(t.Context(), a.State, a.Browser, code)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = approvalService(t, f, []string{"202"}).ApprovalSession(t.Context(), token); !errors.Is(e, integrations.ErrApprovalSession) {
		t.Fatal("removed approver retained session", e)
	}
	if _, e = f.pool.Exec(t.Context(), `UPDATE approval_sessions SET expires_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ApprovalSession(t.Context(), token); !errors.Is(e, integrations.ErrApprovalSession) {
		t.Fatal("expired session accepted", e)
	}
}

func TestApprovalLoginRejectsChangedAppAndTamperedPKCE(t *testing.T) {
	f := setup(t)
	s := approvalService(t, f, []string{"101"})
	a, code := approvalStart(t, s)
	changed := f.config
	changed.ApproverGitHubIDs = []string{"101"}
	changed.GitHub.ClientID = "different-app"
	other, err := integrations.New(f.pool, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = other.CompleteApprovalLogin(t.Context(), a.State, a.Browser, code); !errors.Is(err, integrations.ErrCallback) {
		t.Fatal("changed app accepted", err)
	}
	a, code = approvalStart(t, s)
	if _, err = f.pool.Exec(t.Context(), `UPDATE approval_oauth_states SET verifier=decode('00','hex')`); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = s.CompleteApprovalLogin(t.Context(), a.State, a.Browser, code); err == nil {
		t.Fatal("tampered PKCE accepted")
	}
	var count int
	if err = f.pool.QueryRow(t.Context(), `SELECT count(*) FROM approval_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
