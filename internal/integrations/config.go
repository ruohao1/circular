// Package integrations owns provider authorization, encrypted credentials, and
// importing external resources into the Circular domain.
package integrations

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrConfiguration   = errors.New("integration authentication is not configured")
	ErrReconnect       = errors.New("connection authorization is unavailable; reconnect in Setup")
	ErrProvider        = errors.New("the provider is unavailable; try again")
	ErrAccess          = errors.New("the connected account cannot access this resource")
	ErrCallback        = errors.New("authorization expired or could not be verified; connect again")
	ErrConflict        = errors.New("a resource with this name is already registered in the project")
	ErrEmptyRepository = errors.New("the repository needs a default branch before import")
	ErrIssueTitle      = errors.New("the issue title must contain between 1 and 500 characters")
	ErrAppConfigured   = errors.New("the provider app is already configured")
	ErrAppInUse        = errors.New("disconnect this provider in all Circular projects before changing its app")
	ErrAppInput        = errors.New("enter a valid app setting")
	ErrAppIdentity     = errors.New("the URL must belong to the GitHub App already registered in Circular")
)

type OAuthApp struct {
	ClientID     string
	ClientSecret string `json:"-"`
}

// Endpoint overrides and HTTPClient are dependency-injection seams for owned
// provider fixtures. They are never accepted through HTTP or environment settings.
type Config struct {
	ArtifactRoot         string
	RepositoryCacheRoot  string
	APIURL               string
	WebURL               string
	EncryptionKey        string `json:"-"`
	GitHub               OAuthApp
	Linear               OAuthApp
	GitHubAppSlug        string
	GitHubPrivateKeyFile string `json:"-"`
	githubPrivateKey     string
	HTTPClient           *http.Client `json:"-"`
	GitHubURL            string
	GitHubAPIURL         string
	LinearURL            string
	LinearAPIURL         string
}

func LoadConfig(getenv func(string) string) Config {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	return Config{
		APIURL:               value("CIRCULAR_PUBLIC_API_URL", "http://localhost:8000"),
		WebURL:               value("CIRCULAR_WEB_URL", "http://localhost:5173"),
		EncryptionKey:        getenv("CIRCULAR_INTEGRATIONS_ENCRYPTION_KEY"),
		GitHub:               OAuthApp{getenv("CIRCULAR_GITHUB_CLIENT_ID"), getenv("CIRCULAR_GITHUB_CLIENT_SECRET")},
		GitHubAppSlug:        getenv("CIRCULAR_GITHUB_APP_SLUG"),
		GitHubPrivateKeyFile: getenv("CIRCULAR_GITHUB_PRIVATE_KEY_FILE"),
		Linear:               OAuthApp{getenv("CIRCULAR_LINEAR_CLIENT_ID"), getenv("CIRCULAR_LINEAR_CLIENT_SECRET")},
	}
}

type Service struct {
	pool             *pgxpool.Pool
	config           Config
	vault            cipher.AEAD
	client           *http.Client
	resolvedProvider string
	githubTokens     *githubTokenCache
}

func New(pool *pgxpool.Pool, config Config) (*Service, error) {
	if pool == nil {
		return nil, ErrConfiguration
	}
	defaults := []struct {
		target *string
		value  string
	}{
		{&config.APIURL, "http://localhost:8000"}, {&config.WebURL, "http://localhost:5173"},
		{&config.GitHubURL, "https://github.com"}, {&config.GitHubAPIURL, "https://api.github.com"},
		{&config.LinearURL, "https://linear.app"}, {&config.LinearAPIURL, "https://api.linear.app"},
	}
	for _, entry := range defaults {
		if *entry.target == "" {
			*entry.target = entry.value
		}
		*entry.target = strings.TrimRight(*entry.target, "/")
		u, err := url.Parse(*entry.target)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1"))) {
			return nil, ErrConfiguration
		}
	}
	var vault cipher.AEAD
	if config.EncryptionKey != "" {
		key, err := base64.StdEncoding.DecodeString(config.EncryptionKey)
		if err != nil || len(key) != 32 {
			return nil, ErrConfiguration
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, ErrConfiguration
		}
		vault, err = cipher.NewGCM(block)
		if err != nil {
			return nil, ErrConfiguration
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	if config.HTTPClient != nil {
		copy := *config.HTTPClient
		client = &copy
		if client.Timeout == 0 {
			client.Timeout = 20 * time.Second
		}
	}
	// Provider redirects must never forward bearer credentials to another origin.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Service{pool: pool, config: config, vault: vault, client: client, githubTokens: &githubTokenCache{values: map[string]githubInstallationToken{}}}, nil
}

func (s *Service) app(provider string) OAuthApp {
	switch provider {
	case "github":
		return s.config.GitHub
	case "linear":
		return s.config.Linear
	default:
		return OAuthApp{}
	}
}

func (s *Service) Configured(provider string) bool {
	app := s.app(provider)
	return s.vault != nil && app.ClientID != "" && (provider == "linear" || app.ClientSecret != "")
}

func (s *Service) CallbackURL(provider string) string {
	return s.config.APIURL + "/api/v1/integrations/" + provider + "/callback"
}
func (s *Service) WebURL() string { return s.config.WebURL }
func (s *Service) APIURL() string { return s.config.APIURL }

func (s *Service) seal(value []byte, identity string) ([]byte, error) {
	if s.vault == nil {
		return nil, ErrConfiguration
	}
	nonce := make([]byte, s.vault.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.vault.Seal(nonce, nonce, value, []byte(identity)), nil
}

func (s *Service) open(value []byte, identity string) ([]byte, error) {
	if s.vault == nil || len(value) < s.vault.NonceSize() {
		return nil, ErrReconnect
	}
	data, err := s.vault.Open(nil, value[:s.vault.NonceSize()], value[s.vault.NonceSize():], []byte(identity))
	if err != nil {
		return nil, ErrReconnect
	}
	return data, nil
}
