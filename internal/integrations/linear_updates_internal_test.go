package integrations

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseLinearScopesDoesNotInventGrants(t *testing.T) {
	for _, test := range []struct {
		raw     string
		granted bool
	}{{`"read comments:create"`, true}, {`["read","comments:create"]`, true}, {`"read"`, false}, {`null`, false}, {`[]`, false}, {`""`, false}} {
		scopes, err := parseScopes(json.RawMessage(test.raw))
		if err != nil || commentGrant(scopes) != test.granted {
			t.Fatal(test, scopes, err)
		}
	}
	if _, err := parseScopes(json.RawMessage(`{"read":true}`)); err == nil {
		t.Fatal("malformed scope accepted")
	}
}
func TestLinearSummaryExcludesCodeDiffsAndCredentials(t *testing.T) {
	summary := cleanLinearSummary("Fixed bug.\n\n```go\nprivate source\n```\n\ndiff --git a/private b/private\n@@ -1 +1 @@\n+private source\n\nChecks passed.\nACCESS_TOKEN=fixture-sensitive-value\n@team <img src=x>\n")
	for _, forbidden := range []string{"private source", "fixture-sensitive-value", "@team", "<img"} {
		if strings.Contains(summary, forbidden) {
			t.Fatal("unsafe summary", summary)
		}
	}
	if !strings.Contains(summary, "Checks passed.") {
		t.Fatal("useful summary lost", summary)
	}
}
func TestLinearPullRequestLinksAreStrict(t *testing.T) {
	text := "Done: https://github.com/acme/repo/pull/12. https://github.com/acme/repo/pull/12\nhttps://github.com.evil/acme/repo/pull/42 https://github.com/acme/repo/pull/14?token=secret https://github.com/acme/repo/pull/15#secret https://github.com/acme/repo/pull/16/extra"
	body := linearUpdateBody("http://localhost:5173", linearUpdate{Outcome: "succeeded", Phase: "terminal", Summary: text, Run: "123"})
	parts := strings.Split(body, "Pull requests reported by the agent:")
	if len(parts) != 2 || strings.TrimSpace(parts[1]) != "- https://github.com/acme/repo/pull/12" {
		t.Fatal(body)
	}
}
