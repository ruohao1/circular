package codexworkload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/codexauth"
	"github.com/ruohao1/circular/internal/delegations"
)

func TestDelegationPublishedOnlyAfterCleanExit(t *testing.T) {
	for _, prompt := range []string{"delegate-success", "delegate-failure"} {
		t.Run(prompt, func(t *testing.T) {
			dir := privateAuthDirectory(t)
			saveLogin(t, dir)
			input, _ := json.Marshal(map[string]any{"protocol_version": 1, "auth_mode": "chatgpt", "prompt": prompt, "delegation_targets": []delegations.Target{{AgentID: uuid.MustParse("00000000-0000-4000-8000-000000000001"), Name: "Reviewer"}}})
			var out, diagnostic bytes.Buffer
			code := runAt(t.Context(), bytes.NewReader(input), &out, &diagnostic, program(t), dir)
			if (code == 0) != (prompt == "delegate-success") {
				t.Fatalf("code %d: %s", code, diagnostic.String())
			}
			if got := strings.Count(out.String(), "circular.delegation.requested"); (got == 1) != (code == 0) {
				t.Fatalf("output %s", out.String())
			}
			if strings.Contains(out.String(), "synthetic-access-token") {
				t.Fatal("unredacted delegation")
			}
		})
	}
}

func TestDelegationSequentialInvocationsReleaseAuth(t *testing.T) {
	dir := privateAuthDirectory(t)
	saveLogin(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	invocations := 0
	for _, name := range []string{"parent", "child", "continuation"} {
		done := make(chan int, 1)
		go func() {
			done <- runAt(ctx, bytes.NewReader(subscriptionInput(t, "delegation-lock-proof")), io.Discard, io.Discard, program(t), dir)
		}()
		select {
		case code := <-done:
			if code != 0 {
				t.Fatalf("%s: %d", name, code)
			}
			invocations++
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		released := make(chan error, 1)
		go func() { released <- codexauth.WithLock(ctx, dir, func() error { return nil }) }()
		select {
		case err := <-released:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatalf("%s retained login lock", name)
		}
	}
	if invocations != 3 {
		t.Fatal(invocations)
	}
}

func TestDelegationCatalogRejectsReview(t *testing.T) {
	input := `{"protocol_version":1,"prompt":"review","purpose":"pr_review","review_context_sha256":"` + strings.Repeat("a", 64) + `","delegation_targets":[{"agent_id":"00000000-0000-4000-8000-000000000001","name":"Reviewer"}]}`
	if _, err := parse([]byte(input)); err == nil {
		t.Fatal("review accepted delegation tools")
	}
}

func TestDelegationContinuationVerifiesBeforeCredentials(t *testing.T) {
	input := `{"protocol_version":1,"prompt":"continue","auth_mode":"chatgpt","delegation_context_sha256":"` + strings.Repeat("a", 64) + `"}`
	var diagnostic bytes.Buffer
	if code := runAtContexts(t.Context(), strings.NewReader(input), io.Discard, &diagnostic, program(t), "/nonexistent-auth", t.TempDir(), t.TempDir()); code != 2 || !strings.Contains(diagnostic.String(), "delegation input could not be verified") {
		t.Fatal(code, diagnostic.String())
	}
	withTargets := strings.TrimSuffix(input, "}") + `,"delegation_targets":[{"agent_id":"00000000-0000-4000-8000-000000000001","name":"Reviewer"}]}`
	if _, err := parse([]byte(withTargets)); err == nil {
		t.Fatal("continuation allowed another child")
	}
}
