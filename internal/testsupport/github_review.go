package testsupport

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/ruohao1/circular/internal/prreviews"
)

type FixtureGitHubReview struct{ ID, CommitID, Body, HTMLURL, AuthorID string }

func (p *ProviderFixture) ReviewReceipts() []FixtureGitHubReview {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]FixtureGitHubReview{}, p.reviewReceipts...)
}
func (p *ProviderFixture) ClearReviewPR() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reviewPR = nil
	p.reviewReceipts = nil
}

func (p *ProviderFixture) AddReviewReceipt(r FixtureGitHubReview) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reviewReceipts = append(p.reviewReceipts, r)
}
func reviewJSON(v FixtureGitHubReview) map[string]any {
	return map[string]any{"id": json.Number(v.ID), "commit_id": v.CommitID, "body": v.Body, "html_url": v.HTMLURL, "user": map[string]any{"id": json.Number(v.AuthorID)}}
}

type fixtureReviewPR struct {
	Identity                       prreviews.PRIdentity
	State                          string
	HeadRepository, BaseRepository string
}

func (p *ProviderFixture) SetReviewPR(identity prreviews.PRIdentity, state string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reviewPR = &fixtureReviewPR{identity, state, identity.GitHubRepositoryID, identity.GitHubRepositoryID}
}
func (p *ProviderFixture) SetReviewRepositories(base, head string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reviewPR.BaseRepository = base
	p.reviewPR.HeadRepository = head
}
func (p *ProviderFixture) serveReview(w http.ResponseWriter, r *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reviewPR == nil {
		return false
	}
	f := p.reviewPR
	v := f.Identity
	switch r.URL.Path {
	case "/user":
		if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_") {
			w.WriteHeader(403)
			return true
		}
		fixtureJSON(w, map[string]any{"id": 7, "login": "fixture", "html_url": "https://github.com/fixture"})
	case "/repositories/" + v.GitHubRepositoryID:
		fixtureJSON(w, map[string]any{"id": json.Number(v.GitHubRepositoryID), "full_name": v.RepositoryName, "default_branch": v.BaseRef, "private": true, "clone_url": "https://github.com/" + v.RepositoryName + ".git", "html_url": "https://github.com/" + v.RepositoryName})
	case "/user/installations":
		if p.ReviewPermissionDenied.Load() {
			http.Error(w, "revoked", 403)
			return true
		}
		fixtureJSON(w, map[string]any{"total_count": 1, "installations": []any{map[string]any{"id": json.Number(v.InstallationID), "permissions": map[string]string{"contents": "write", "pull_requests": "write"}, "account": map[string]any{"login": "fixture", "id": 7, "type": "User"}, "app_id": 123, "app_slug": "circular-fixture"}}})
	case "/user/installations/" + v.InstallationID + "/repositories":
		fixtureJSON(w, map[string]any{"total_count": 1, "repositories": []any{map[string]any{"id": json.Number(v.GitHubRepositoryID), "full_name": v.RepositoryName, "default_branch": v.BaseRef, "private": true, "clone_url": "https://github.com/" + v.RepositoryName + ".git", "html_url": "https://github.com/" + v.RepositoryName}}})
	case "/repos/" + v.RepositoryName + "/pulls/" + strconv.Itoa(v.Number) + "/reviews":
		if r.Method == http.MethodPost {
			p.ReviewCreates.Add(1)
			if status := int(p.ReviewReject.Load()); status != 0 {
				w.Header().Set("Retry-After", "120")
				http.Error(w, "fixture rejection", status)
				return true
			}
			var body struct {
				Body, Event string
				CommitID    string `json:"commit_id"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Event != "COMMENT" || body.CommitID != v.HeadSHA {
				http.Error(w, "invalid review", 422)
				return true
			}
			id := strconv.Itoa(1000 + len(p.reviewReceipts))
			receipt := FixtureGitHubReview{id, body.CommitID, body.Body, v.URL + "#pullrequestreview-" + id, "7"}
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ghs_") {
				receipt.AuthorID = "501"
			}
			p.reviewReceipts = append(p.reviewReceipts, receipt)
			if p.LoseReviewResponse.Swap(false) {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					connection.Close()
				}
				return true
			}
			fixtureJSON(w, reviewJSON(receipt))
			return true
		}
		p.ReviewLists.Add(1)
		values := []any{}
		if p.HideReviews.Load() {
			fixtureJSON(w, values)
			return true
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		start := (page - 1) * 2
		for i := start; i < len(p.reviewReceipts) && i < start+2; i++ {
			values = append(values, reviewJSON(p.reviewReceipts[i]))
		}
		if start+2 < len(p.reviewReceipts) {
			w.Header().Set("Link", fmt.Sprintf("<http://%s%s?per_page=100&page=%d>; rel=\"next\"", r.Host, r.URL.Path, page+1))
		}
		fixtureJSON(w, values)
	case "/repos/" + v.RepositoryName + "/pulls/" + strconv.Itoa(v.Number):
		p.ReviewReads.Add(1)
		if status := int(p.ReviewReadReject.Load()); status != 0 {
			w.Header().Set("Retry-After", "180")
			http.Error(w, "fixture rate limit", status)
			return true
		}
		state := f.State
		if state == "merged" {
			state = "closed"
		}
		fixtureJSON(w, map[string]any{"number": v.Number, "html_url": v.URL, "state": state, "merged": f.State == "merged", "base": map[string]any{"ref": v.BaseRef, "sha": v.BaseSHA, "repo": map[string]any{"id": json.Number(f.BaseRepository)}}, "head": map[string]any{"ref": v.HeadRef, "sha": v.HeadSHA, "repo": map[string]any{"id": json.Number(f.HeadRepository)}}})
	default:
		return false
	}
	return true
}
