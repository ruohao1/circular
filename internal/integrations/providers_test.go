package integrations

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLinearRevokedRefreshRequiresReconnect(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"revoked", 400, `{"error":"invalid_request","error_description":"Refresh token revoked"}`, ErrReconnect},
		{"other invalid request", 400, `{"error":"invalid_request","error_description":"Missing client_id"}`, ErrProvider},
		{"provider failure", 500, `{"error":"invalid_request","error_description":"Refresh token revoked"}`, ErrProvider},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			pool, err := pgxpool.New(t.Context(), "postgresql://test:test@127.0.0.1:1/test")
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			service, err := New(pool, Config{LinearAPIURL: server.URL, Linear: OAuthApp{ClientID: "fixture-linear"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.token(t.Context(), "linear", "", "", "fixture-refresh"); !errors.Is(err, test.want) {
				t.Fatalf("refresh error = %v; want %v", err, test.want)
			}
			if err := service.linear(t.Context(), "fixture-token", "query { viewer { id } }", nil, &map[string]any{}); !errors.Is(err, ErrProvider) {
				t.Fatalf("non-token request must retain provider error: %v", err)
			}
		})
	}
}

func TestProviderRedirectsDoNotReceiveCredentials(t *testing.T) {
	var received bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	pool, err := pgxpool.New(context.Background(), "postgresql://test:test@127.0.0.1:1/test")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	service, err := New(pool, Config{GitHubAPIURL: redirect.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.github(t.Context(), "fixture-secret", "/user", &map[string]any{}); !errors.Is(err, ErrProvider) {
		t.Fatal(err)
	}
	if received {
		t.Fatal("followed a credential-bearing redirect")
	}
}
