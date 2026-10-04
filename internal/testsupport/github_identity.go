package testsupport

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type FixtureInstallationScope struct {
	RepositoryIDs []int64           `json:"repository_ids"`
	Permissions   map[string]string `json:"permissions"`
}

func (p *ProviderFixture) GitHubPrivateKey() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.githubKey == nil {
		var err error
		p.githubKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(p.githubKey)})
}
func (p *ProviderFixture) InstallationScopes() []FixtureInstallationScope {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]FixtureInstallationScope{}, p.installationScopes...)
}

func (p *ProviderFixture) serveGitHubIdentity(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/users/") && strings.HasSuffix(r.URL.Path, "[bot]") {
		p.mu.Lock()
		slug := p.githubSlug
		p.mu.Unlock()
		fixtureJSON(w, map[string]any{"id": 501, "login": slug + "[bot]", "name": "Circular fixture bot", "avatar_url": "https://avatars.githubusercontent.com/u/501"})
		return true
	}
	if r.URL.Path != "/app/hook/config" && r.URL.Path != "/app" && !strings.HasPrefix(r.URL.Path, "/app/installations/") {
		return false
	}
	p.GitHubPrivateKey()
	parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
	valid := len(parts) == 3
	if valid {
		var claims struct {
			Iss      string `json:"iss"`
			Iat, Exp int64
		}
		body, _ := base64.RawURLEncoding.DecodeString(parts[1])
		signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		p.mu.Lock()
		key := p.githubKey
		p.mu.Unlock()
		valid = json.Unmarshal(body, &claims) == nil && claims.Iss == "fixture-github" && claims.Exp > time.Now().Unix() && claims.Exp <= time.Now().Add(10*time.Minute).Unix() && claims.Iat <= time.Now().Unix() && rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], signature) == nil
	}
	if !valid {
		w.WriteHeader(401)
		return true
	}
	if r.URL.Path == "/app/hook/config" {
		p.mu.Lock()
		defer p.mu.Unlock()
		if r.Method == "PATCH" {
			var config map[string]string
			if json.NewDecoder(r.Body).Decode(&config) != nil {
				w.WriteHeader(400)
				return true
			}
			p.githubHookURL = config["url"]
		}
		fixtureJSON(w, map[string]string{"url": p.githubHookURL, "content_type": "json", "insecure_ssl": "0"})
		return true
	}
	p.mu.Lock()
	slug := p.githubSlug
	p.mu.Unlock()
	if r.URL.Path == "/app" {
		client := "fixture-github"
		if p.WrongGitHubApp.Load() {
			client = "wrong-app"
		}
		fixtureJSON(w, map[string]any{"id": 123, "client_id": client, "slug": slug})
		return true
	}
	p.mu.Lock()
	installationID, repositoryID := int64(101), int64(202)
	if p.reviewPR != nil {
		installationID, _ = strconv.ParseInt(p.reviewPR.Identity.InstallationID, 10, 64)
		repositoryID, _ = strconv.ParseInt(p.reviewPR.Identity.GitHubRepositoryID, 10, 64)
	}
	p.mu.Unlock()
	installationPath := "/app/installations/" + strconv.FormatInt(installationID, 10)
	if r.URL.Path == installationPath {
		var suspended any
		if p.SuspendedInstallation.Load() {
			suspended = time.Now().Format(time.RFC3339)
		}
		if p.ReviewPermissionDenied.Load() {
			w.WriteHeader(403)
			return true
		}
		fixtureJSON(w, map[string]any{"id": installationID, "app_id": 123, "account": map[string]any{"login": "fixture"}, "suspended_at": suspended, "permissions": map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}})
		return true
	}
	if r.URL.Path == installationPath+"/access_tokens" && r.Method == "POST" {
		var scope FixtureInstallationScope
		if json.NewDecoder(r.Body).Decode(&scope) != nil || len(scope.RepositoryIDs) != 1 || scope.RepositoryIDs[0] != repositoryID || p.MissingInstallationRepository.Load() {
			w.WriteHeader(403)
			return true
		}
		token := "ghs_fixture_" + uuid.NewString()
		p.mu.Lock()
		p.tokens[token] = "github"
		p.installationScopes = append(p.installationScopes, scope)
		p.mu.Unlock()
		expires := time.Now().Add(time.Hour)
		if p.ExpiredInstallationToken.Load() {
			expires = time.Now().Add(-time.Hour)
		}
		fixtureJSON(w, map[string]any{"token": token, "expires_at": expires, "permissions": scope.Permissions, "repositories": []any{map[string]any{"id": repositoryID}}})
		return true
	}
	w.WriteHeader(404)
	return true
}
