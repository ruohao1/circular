package testsupport

import (
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

const ProviderTeamID = "20000000-0000-4000-8000-000000000001"
const ProviderProjectID = "20000000-0000-4000-8000-000000000002"
const ProviderIssueID = "20000000-0000-4000-8000-000000000003"

// ProviderFixture is used only by tests and the disposable browser-test stack.
// No production configuration can redirect OAuth/provider endpoints to it.
type ProviderFixture struct {
	BeforeLinearActivity func()
	activities           map[string]FixtureLinearActivity
	agentLinks           map[string][]map[string]string
	LoseActivityResponse atomic.Bool
	HideActivities       atomic.Bool
	ActivityReject       atomic.Int64
	ActivityCreates      atomic.Int64

	agentSessions                 map[string]map[string]any
	githubHookURL                 string
	githubKey                     *rsa.PrivateKey
	installationScopes            []FixtureInstallationScope
	WrongGitHubApp                atomic.Bool
	SuspendedInstallation         atomic.Bool
	MissingInstallationRepository atomic.Bool
	ExpiredInstallationToken      atomic.Bool
	mu                            sync.Mutex
	codes                         map[string]fixtureCode
	tokens                        map[string]string
	actors                        map[string]string
	refresh                       map[string]string
	manifests                     map[string]fixtureManifest
	setupURL                      string
	githubSlug                    string
	linearScopes                  string
	linearOrg                     string
	comments                      map[string]FixtureLinearComment
	LoseCommentResponse           atomic.Bool
	OmitRefreshScope              atomic.Bool
	RejectComments                atomic.Bool
	CommentCreates                atomic.Int64
	BeforeLinearComment           func()
	Conversions                   atomic.Int64
	Refreshes                     atomic.Int64
	Exchanges                     atomic.Int64
	Unavailable                   atomic.Bool
	reviewPR                      *fixtureReviewPR
	reviewReceipts                []FixtureGitHubReview
	LoseReviewResponse            atomic.Bool
	HideReviews                   atomic.Bool
	ReviewCreates                 atomic.Int64
	ReviewLists                   atomic.Int64
	ReviewReads                   atomic.Int64
	ReviewReadReject              atomic.Int64
	ReviewReject                  atomic.Int64
	ReviewPermissionDenied        atomic.Bool
	githubCreation                FixtureGitHubCreation
	githubCreated                 []FixtureGitHubRepository
	RepositoryCreates             atomic.Int64
}
type fixtureCode struct{ provider, challenge, redirect, actor string }

type FixtureLinearComment struct {
	ID      string `json:"id"`
	IssueID string `json:"issue_id"`
	Body    string `json:"body"`
	ActorID string `json:"actor_id"`
}

func (p *ProviderFixture) LinearComments() []FixtureLinearComment {
	p.mu.Lock()
	defer p.mu.Unlock()
	items := []FixtureLinearComment{}
	for _, c := range p.comments {
		items = append(items, c)
	}
	return items
}
func (p *ProviderFixture) SetLinearScopes(scopes string) {
	p.mu.Lock()
	p.linearScopes = scopes
	p.mu.Unlock()
}
func (p *ProviderFixture) SetLinearOrganization(id string) {
	p.mu.Lock()
	p.linearOrg = id
	p.mu.Unlock()
}

type fixtureManifest struct {
	Redirect    string            `json:"redirect_url"`
	Setup       string            `json:"setup_url"`
	Permissions map[string]string `json:"default_permissions"`
	Hook        *struct {
		URL string `json:"url"`
	} `json:"hook_attributes"`
}

func NewProviderFixture() *ProviderFixture {
	return &ProviderFixture{codes: map[string]fixtureCode{}, tokens: map[string]string{}, refresh: map[string]string{}, manifests: map[string]fixtureManifest{}, githubSlug: "circular-fixture", linearScopes: "read comments:create", linearOrg: "20000000-0000-4000-8000-000000000004", comments: map[string]FixtureLinearComment{}, githubCreation: FixtureGitHubCreation{Administration: "write", AccountType: "User", Account: "circular-fixture", Attach: true}}
}

func (p *ProviderFixture) RenameGitHubApp(slug string) {
	p.mu.Lock()
	p.githubSlug = slug
	p.mu.Unlock()
}

// ManifestCode models the code GitHub returns after the user creates an app.
func (p *ProviderFixture) ManifestCode(manifest string) (string, error) {
	var value fixtureManifest
	if err := json.Unmarshal([]byte(manifest), &value); err != nil {
		return "", err
	}
	// GitHub validates a supplied webhook URL even when active is false.
	// Browser callback URLs may remain local; webhook delivery cannot reach them.
	if value.Hook != nil {
		hook, err := url.Parse(value.Hook.URL)
		if err != nil || hook.Hostname() == "" || (hook.Scheme != "http" && hook.Scheme != "https") {
			return "", fmt.Errorf("Hook url is invalid")
		}
		host := strings.ToLower(strings.TrimSuffix(hook.Hostname(), "."))
		address, _ := netip.ParseAddr(host)
		if host == "localhost" || strings.HasSuffix(host, ".localhost") || address.IsLoopback() || address.IsPrivate() || address.IsUnspecified() || address.IsLinkLocalUnicast() {
			return "", fmt.Errorf("Hook url is not supported because it isn't reachable over the public Internet (%s)", host)
		}
	}
	code := uuid.NewString()
	p.mu.Lock()
	p.manifests[code] = value
	p.mu.Unlock()
	return code, nil
}

func fixtureJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (p *ProviderFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.githubCreationControl(w, r) {
		return
	}
	if r.URL.Path == "/fixture/linear/scopes" && r.Method == "POST" {
		var body struct {
			Scopes string `json:"scopes"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "invalid scopes", 400)
			return
		}
		p.SetLinearScopes(body.Scopes)
		fixtureJSON(w, map[string]bool{"saved": true})
		return
	}
	if r.URL.Path == "/fixture/linear/comments" && r.Method == "GET" {
		fixtureJSON(w, map[string]any{"items": p.LinearComments()})
		return
	}
	if p.Unavailable.Load() {
		http.Error(w, "fixture unavailable", 503)
		return
	}
	if p.serveGitHubIdentity(w, r) {
		return
	}
	if p.serveReview(w, r) {
		return
	}
	if r.URL.Path == "/fixture/github/rename" && r.Method == "POST" {
		var body struct {
			Slug string `json:"slug"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Slug == "" {
			http.Error(w, "invalid fixture slug", 400)
			return
		}
		p.RenameGitHubApp(body.Slug)
		fixtureJSON(w, map[string]bool{"renamed": true})
		return
	}
	p.mu.Lock()
	slug := p.githubSlug
	p.mu.Unlock()
	if r.URL.Path == "/apps/"+slug || r.URL.Path == "/apps/another-fixture-app" {
		clientID := "fixture-github"
		if r.URL.Path == "/apps/another-fixture-app" {
			clientID = "another-fixture-client"
			slug = "another-fixture-app"
		}
		fixtureJSON(w, map[string]any{"id": 123, "client_id": clientID, "slug": slug})
		return
	}
	if r.URL.Path == "/settings/installations/101" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<h1>Manage GitHub fixture access</h1>`)
		return
	}
	if r.Method == "POST" && (r.URL.Path == "/settings/apps/new" || strings.HasSuffix(r.URL.Path, "/settings/apps/new") && strings.HasPrefix(r.URL.Path, "/organizations/")) {
		_ = r.ParseForm()
		code, err := p.ManifestCode(r.Form.Get("manifest"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		p.mu.Lock()
		manifest := p.manifests[code]
		p.mu.Unlock()
		callback, err := url.Parse(manifest.Redirect)
		if err != nil {
			http.Error(w, "invalid callback", 400)
			return
		}
		query := callback.Query()
		query.Set("code", code)
		query.Set("state", r.URL.Query().Get("state"))
		callback.RawQuery = query.Encode()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<h1>GitHub fixture</h1><a href="%s">Create GitHub App</a>`, html.EscapeString(callback.String()))
		return
	}
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/app-manifests/") && strings.HasSuffix(r.URL.Path, "/conversions") {
		code := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/app-manifests/"), "/conversions")
		p.mu.Lock()
		manifest, ok := p.manifests[code]
		delete(p.manifests, code)
		if ok {
			p.setupURL = manifest.Setup
		}
		p.mu.Unlock()
		if !ok {
			http.Error(w, "invalid manifest code", 404)
			return
		}
		p.Conversions.Add(1)
		fixtureJSON(w, map[string]any{"id": 123, "slug": slug, "client_id": "fixture-github", "client_secret": "fixture-github-secret", "permissions": manifest.Permissions, "pem": string(p.GitHubPrivateKey()), "webhook_secret": "fixture-webhook-secret"})
		return
	}
	if r.URL.Path == "/apps/"+slug+"/installations/new" {
		p.mu.Lock()
		setup := p.setupURL
		p.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<h1>Install GitHub fixture</h1><p>Selected repository: fixture/private-source</p><a href="%s">Install selected repositories</a>`, html.EscapeString(setup))
		return
	}
	if r.URL.Path == "/fixture/revoke" && r.Method == "POST" {
		p.mu.Lock()
		clear(p.tokens)
		clear(p.refresh)
		p.mu.Unlock()
		fixtureJSON(w, map[string]bool{"revoked": true})
		return
	}
	if r.URL.Path == "/login/oauth/authorize" || r.URL.Path == "/oauth/authorize" {
		provider := "linear"
		if r.URL.Path == "/login/oauth/authorize" {
			provider = "github"
		}
		query := r.URL.Query()
		if query.Get("state") == "" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
			http.Error(w, "invalid authorization request", 400)
			return
		}
		code := uuid.NewString()
		p.mu.Lock()
		p.codes[code] = fixtureCode{provider, query.Get("code_challenge"), query.Get("redirect_uri"), query.Get("actor")}
		p.mu.Unlock()
		callback, err := url.Parse(query.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "invalid callback", 400)
			return
		}
		values := callback.Query()
		values.Set("state", query.Get("state"))
		values.Set("code", code)
		callback.RawQuery = values.Encode()
		http.Redirect(w, r, callback.String(), http.StatusSeeOther)
		return
	}
	if r.URL.Path == "/login/oauth/access_token" || r.URL.Path == "/oauth/token" {
		provider := "linear"
		if r.URL.Path == "/login/oauth/access_token" {
			provider = "github"
		}
		_ = r.ParseForm()
		secret := r.Form.Get("client_secret")
		validSecret := secret == "fixture-"+provider+"-secret" || provider == "linear" && secret == ""
		if r.Form.Get("client_id") != "fixture-"+provider || !validSecret {
			w.WriteHeader(400)
			fixtureJSON(w, map[string]string{"error": "invalid_client"})
			return
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		actor := "user"
		if p.actors == nil {
			p.actors = map[string]string{}
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			old := p.refresh[r.Form.Get("refresh_token")]
			actor = p.actors[old]
			if old == "" || p.tokens[old] != provider {
				w.WriteHeader(400)
				fixtureJSON(w, map[string]string{"error": "invalid_grant"})
				return
			}
			delete(p.refresh, r.Form.Get("refresh_token"))
			delete(p.tokens, old)
			p.Refreshes.Add(1)
		} else {
			code, ok := p.codes[r.Form.Get("code")]
			actor = code.actor
			challenge := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || code.provider != provider || code.redirect != r.Form.Get("redirect_uri") || code.challenge != base64.RawURLEncoding.EncodeToString(challenge[:]) {
				w.WriteHeader(400)
				fixtureJSON(w, map[string]string{"error": "invalid_grant"})
				return
			}
			delete(p.codes, r.Form.Get("code"))
			p.Exchanges.Add(1)
		}
		access, refresh := "fixture_access_"+uuid.NewString(), "fixture_refresh_"+uuid.NewString()
		if provider == "github" {
			access = "ghu_" + access
		}
		p.tokens[access], p.refresh[refresh] = provider, access
		p.actors[access] = actor
		response := map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3600, "token_type": "bearer", "scope": p.linearScopes}
		if r.Form.Get("grant_type") == "refresh_token" && p.OmitRefreshScope.Load() {
			delete(response, "scope")
		}
		fixtureJSON(w, response)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/applications/") && r.Method == "DELETE" {
		user, secret, ok := r.BasicAuth()
		if !ok || user != "fixture-github" || secret != "fixture-github-secret" {
			w.WriteHeader(401)
			return
		}
		var body struct {
			Token string `json:"access_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		delete(p.tokens, body.Token)
		for refresh, token := range p.refresh {
			if token == body.Token {
				delete(p.refresh, refresh)
			}
		}
		p.mu.Unlock()
		w.WriteHeader(204)
		return
	}
	if r.URL.Path == "/oauth/revoke" {
		_ = r.ParseForm()
		token := r.Form.Get("token")
		p.mu.Lock()
		if access := p.refresh[token]; access != "" {
			delete(p.tokens, access)
			delete(p.refresh, token)
		} else {
			delete(p.tokens, token)
		}
		p.mu.Unlock()
		w.WriteHeader(200)
		return
	}
	access := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	provider := p.tokens[access]
	p.mu.Unlock()
	if provider == "" {
		w.WriteHeader(401)
		fixtureJSON(w, map[string]string{"error": "unauthorized"})
		return
	}
	if provider == "github" {
		p.serveGitHub(w, r, slug)
		return
	}
	if r.URL.Path != "/graphql" {
		w.WriteHeader(404)
		return
	}
	var input struct {
		Query     string `json:"query"`
		Variables struct {
			ID    string `json:"id"`
			Input struct {
				ID             string              `json:"id"`
				IssueID        string              `json:"issueId"`
				Body           string              `json:"body"`
				AgentSessionID string              `json:"agentSessionId"`
				Content        json.RawMessage     `json:"content"`
				ExternalURLs   []map[string]string `json:"externalUrls"`
			} `json:"input"`
		} `json:"variables"`
	}
	_ = json.NewDecoder(r.Body).Decode(&input)
	var agentVariables map[string]json.RawMessage
	// Decode only the variables needed by the isolated agent API fixture.
	body, _ := json.Marshal(input.Variables)
	_ = json.Unmarshal(body, &agentVariables)
	if p.serveLinearAgent(w, access, input.Query, agentVariables) {
		return
	}
	page := func(nodes any) map[string]any {
		return map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""}}
	}
	issue := map[string]any{"id": ProviderIssueID, "identifier": "TST-1", "title": "Imported integration task", "description": "Verify a task imported from Linear.", "url": "https://linear.app/circular-fixture/issue/TST-1/imported-task", "state": map[string]string{"name": "Todo"}}
	var data map[string]any
	switch {
	case strings.Contains(input.Query, "CircularAppIdentity"):
		p.mu.Lock()
		org := p.linearOrg
		actor := p.actors[access]
		p.mu.Unlock()
		id, name := "20000000-0000-4000-8000-000000000006", "Fixture user"
		if actor == "app" {
			id, name = "20000000-0000-4000-8000-000000000005", "Circular"
		}
		data = map[string]any{"organization": map[string]string{"id": org, "name": "Circular fixture", "urlKey": "circular-fixture"}, "viewer": map[string]string{"id": id, "name": name, "avatarUrl": ""}}
	case strings.Contains(input.Query, "CircularDeliveredComment"):
		p.mu.Lock()
		comment, ok := p.comments[input.Variables.ID]
		p.mu.Unlock()
		nodes := []any{}
		if ok {
			nodes = append(nodes, map[string]any{"id": comment.ID, "body": comment.Body, "user": map[string]string{"id": comment.ActorID}, "issue": map[string]string{"id": comment.IssueID}})
		}
		data = map[string]any{"comments": page(nodes)}
	case strings.Contains(input.Query, "CircularRunComment"):
		if p.BeforeLinearComment != nil {
			p.BeforeLinearComment()
		}
		if p.RejectComments.Load() {
			fixtureJSON(w, map[string]any{"errors": []any{map[string]any{"message": "fixture permission denied", "extensions": map[string]string{"code": "FORBIDDEN"}}}})
			return
		}
		value := input.Variables.Input
		if _, err := uuid.Parse(value.ID); err != nil || value.IssueID != ProviderIssueID || value.Body == "" {
			http.Error(w, "bad comment", 400)
			return
		}
		p.mu.Lock()
		_, exists := p.comments[value.ID]
		actor := "20000000-0000-4000-8000-000000000006"
		if p.actors[access] == "app" {
			actor = "20000000-0000-4000-8000-000000000005"
		}
		if !exists {
			p.comments[value.ID] = FixtureLinearComment{value.ID, value.IssueID, value.Body, actor}
			p.CommentCreates.Add(1)
		}
		p.mu.Unlock()
		if exists {
			fixtureJSON(w, map[string]any{"errors": []any{map[string]any{"message": "duplicate comment identifier"}}})
			return
		}
		if p.LoseCommentResponse.Swap(false) {
			http.Error(w, "response lost after accepted mutation", 503)
			return
		}
		data = map[string]any{"commentCreate": map[string]any{"success": true, "comment": map[string]any{"id": value.ID, "body": value.Body, "user": map[string]string{"id": actor}, "issue": map[string]string{"id": value.IssueID}}}}
	case strings.Contains(input.Query, "CircularIdentity"):
		p.mu.Lock()
		org := p.linearOrg
		p.mu.Unlock()
		data = map[string]any{"organization": map[string]string{"id": org, "name": "Circular fixture", "urlKey": "circular-fixture"}}
	case strings.Contains(input.Query, "CircularTeams"):
		data = map[string]any{"teams": page([]any{map[string]string{"id": ProviderTeamID, "name": "Engineering", "key": "TST"}})}
	case strings.Contains(input.Query, "CircularProjects"):
		data = map[string]any{"projects": page([]any{map[string]string{"id": ProviderProjectID, "name": "Integration work"}})}
	case strings.Contains(input.Query, "CircularIssues"):
		data = map[string]any{"issues": page([]any{issue})}
	case strings.Contains(input.Query, "CircularIssue") && strings.Contains(input.Query, ProviderIssueID):
		data = map[string]any{"issue": issue}
	default:
		fixtureJSON(w, map[string]any{"errors": []any{map[string]any{"message": "fixture query not recognized"}}})
		return
	}
	fixtureJSON(w, map[string]any{"data": data})
}
