package testsupport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// FixtureGitHubCreation controls only the owned provider fixture, never GitHub.
type FixtureGitHubCreation struct {
	Administration string `json:"administration"`
	Contents       string `json:"contents"`
	PullRequests   string `json:"pull_requests"`
	AccountType    string `json:"account_type"`
	Account        string `json:"account"`
	Attach         bool   `json:"attach"`
	RejectStatus   int    `json:"reject_status"`
	LoseResponse   bool   `json:"lose_response"`
	Malformed      bool   `json:"malformed"`
	ResetCreated   bool   `json:"reset_created"`
}

type FixtureGitHubRepository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Owner         string `json:"owner"`
	Private       bool   `json:"private"`
	AutoInit      bool   `json:"auto_init"`
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
}

func (p *ProviderFixture) SetGitHubCreation(config FixtureGitHubCreation) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if config.ResetCreated {
		p.githubCreated = nil
		p.RepositoryCreates.Store(0)
		config.ResetCreated = false
	}
	p.githubCreation = config
}

func (p *ProviderFixture) GitHubCreated() []FixtureGitHubRepository {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]FixtureGitHubRepository{}, p.githubCreated...)
}

func (p *ProviderFixture) githubCreationControl(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path == "/fixture/github/creation" && r.Method == "POST" {
		p.mu.Lock()
		config := p.githubCreation
		p.mu.Unlock()
		if json.NewDecoder(r.Body).Decode(&config) != nil {
			http.Error(w, "invalid fixture configuration", 400)
			return true
		}
		p.SetGitHubCreation(config)
		fixtureJSON(w, map[string]bool{"saved": true})
		return true
	}
	if r.URL.Path == "/fixture/github/created" && r.Method == "GET" {
		fixtureJSON(w, map[string]any{"items": p.GitHubCreated(), "creates": p.RepositoryCreates.Load()})
		return true
	}
	return false
}

func (p *ProviderFixture) serveGitHub(w http.ResponseWriter, r *http.Request, slug string) {
	p.mu.Lock()
	config := p.githubCreation
	p.mu.Unlock()
	if config.Contents == "" {
		config.Contents = "read"
	}
	repository := map[string]any{"id": 202, "full_name": "fixture/private-source", "default_branch": "main", "private": true}
	switch r.URL.Path {
	case "/user":
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_") {
			w.WriteHeader(403)
			return
		}
		fixtureJSON(w, map[string]any{"id": 101, "login": "circular-fixture"})
		return
	case "/user/installations":
		fixtureJSON(w, map[string]any{"total_count": 1, "installations": []any{map[string]any{"id": 101, "app_slug": slug, "account": map[string]string{"login": config.Account, "type": config.AccountType}, "permissions": map[string]string{"contents": config.Contents, "pull_requests": config.PullRequests, "metadata": "read", "administration": config.Administration}}}})
		return
	case "/user/installations/101/repositories":
		items := []any{repository}
		if config.Attach {
			for _, item := range p.GitHubCreated() {
				items = append(items, item)
			}
		}
		fixtureJSON(w, map[string]any{"total_count": len(items), "repositories": items})
		return
	case "/repositories/202":
		fixtureJSON(w, repository)
		return
	}
	if r.Method == "POST" && (r.URL.Path == "/user/repos" || r.URL.Path == "/orgs/"+config.Account+"/repos") {
		p.RepositoryCreates.Add(1)
		if config.RejectStatus != 0 {
			w.WriteHeader(config.RejectStatus)
			fixtureJSON(w, map[string]string{"message": "creation rejected"})
			return
		}
		if config.Administration != "write" {
			w.WriteHeader(403)
			return
		}
		var input FixtureGitHubRepository
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(422)
			return
		}
		owner := "circular-fixture"
		if strings.HasPrefix(r.URL.Path, "/orgs/") {
			owner = config.Account
		}
		input.Owner = owner
		input.FullName = owner + "/" + input.Name
		p.mu.Lock()
		for _, existing := range p.githubCreated {
			if strings.EqualFold(existing.FullName, input.FullName) {
				p.mu.Unlock()
				w.WriteHeader(422)
				return
			}
		}
		input.ID = int64(300 + len(p.githubCreated))
		if input.AutoInit {
			input.DefaultBranch = "main"
		}
		p.githubCreated = append(p.githubCreated, input)
		p.mu.Unlock()
		if config.LoseResponse {
			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, err := hijacker.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(201)
		if config.Malformed {
			fixtureJSON(w, map[string]any{"id": input.ID, "full_name": "other/unexpected"})
			return
		}
		fixtureJSON(w, input)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repos/") {
		name := strings.TrimPrefix(r.URL.Path, "/repos/")
		if strings.EqualFold(name, "fixture/private-source") {
			fixtureJSON(w, repository)
			return
		}
		for _, item := range p.GitHubCreated() {
			if strings.EqualFold(name, item.FullName) {
				fixtureJSON(w, item)
				return
			}
		}
		w.WriteHeader(404)
		return
	}
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/repositories/") {
		for _, item := range p.GitHubCreated() {
			if r.URL.Path == fmt.Sprintf("/repositories/%d", item.ID) {
				fixtureJSON(w, item)
				return
			}
		}
	}
	w.WriteHeader(403)
}
