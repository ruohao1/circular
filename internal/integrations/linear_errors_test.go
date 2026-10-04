package integrations

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLinearGraphQLPermissionRejection(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"missing write scope", 400, `{"errors":[{"message":"Invalid scope: write required","extensions":{"code":"FORBIDDEN"}}]}`, ErrAccess},
		{"null data rejection", 400, `{"data":null,"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, ErrAccess},
		{"expired authentication", 400, `{"errors":[{"extensions":{"code":"UNAUTHENTICATED"}}]}`, ErrReconnect},
		{"authentication error", 400, `{"errors":[{"extensions":{"code":"AUTHENTICATION_ERROR"}}]}`, ErrReconnect},
		{"multiple permission errors", 400, `{"errors":[{"extensions":{"code":"FORBIDDEN"}},{"extensions":{"code":"FORBIDDEN"}}]}`, ErrAccess},
		{"partial mutation on bad request", 400, `{"data":{"agentActivityCreate":{"success":true}},"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, ErrProvider},
		{"partial mutation on success", 200, `{"data":{"agentActivityCreate":{"success":true}},"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, ErrProvider},
		{"field error after execution", 400, `{"data":null,"errors":[{"path":["agentActivityCreate","agentActivity"],"extensions":{"code":"FORBIDDEN"}}]}`, ErrProvider},
		{"field error on success", 200, `{"data":null,"errors":[{"path":["agentActivityCreate","agentActivity"],"extensions":{"code":"FORBIDDEN"}}]}`, ErrProvider},
		{"mixed errors", 400, `{"errors":[{"extensions":{"code":"FORBIDDEN"}},{"extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, ErrProvider},
		{"mixed errors on success", 200, `{"errors":[{"extensions":{"code":"FORBIDDEN"}},{"extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`, ErrProvider},
		{"server failure remains ambiguous", 503, `{"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, ErrProvider},
		{"malformed response", 400, `{"errors":[`, ErrProvider},
		{"no structured error", 400, `{"error":"FORBIDDEN"}`, ErrProvider},
		{"bad request with success data", 400, `{"data":{"agentActivityCreate":{"success":true}}}`, ErrProvider},
		{"success status rejection", 200, `{"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, ErrAccess},
		{"successful mutation", 200, `{"data":{"agentActivityCreate":{"success":true}}}`, nil},
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
			service, err := New(pool, Config{LinearAPIURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.linear(t.Context(), "fixture-token", "mutation { agentActivityCreate { success } }", nil, &map[string]any{}); !errors.Is(err, test.want) {
				t.Fatalf("GraphQL error = %v; want %v", err, test.want)
			}
		})
	}
}
