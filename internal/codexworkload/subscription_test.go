package codexworkload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/codexauth"
)

const savedLogin = `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"synthetic-id-token","access_token":"synthetic-access-token","refresh_token":"synthetic-refresh-token","account_id":"synthetic-account"}}`

func saveLogin(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(savedLogin), 0600); err != nil {
		t.Fatal(err)
	}
}

func subscriptionInput(t *testing.T, prompt string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"protocol_version": 1, "auth_mode": "chatgpt", "prompt": prompt})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func refreshHelper(prompt string) int {
	dir := os.Getenv("CODEX_HOME")
	secrets, err := codexauth.Secrets(dir)
	if err != nil || os.Getenv("CODEX_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		return 95
	}
	event := func(tokens []string) error {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": strings.Join(tokens, " ")},
			"environment": os.Environ(), "args": os.Args[1:], "home": os.Getenv("HOME"), "prompt": prompt,
			"nested": map[string]any{tokens[0]: []any{map[string]any{"secret": tokens[len(tokens)-1]}}},
		})
	}
	if event(secrets) != nil {
		return 96
	}
	updated := make([]string, len(secrets))
	for i, secret := range secrets {
		updated[i] = secret + "-refreshed"
	}
	data, err := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]any{"id_token": updated[0], "access_token": updated[1], "refresh_token": updated[2]}})
	if err != nil || os.WriteFile(filepath.Join(dir, "auth.json"), data, 0600) != nil {
		return 97
	}
	if event(append(secrets, updated...)) != nil {
		return 98
	}
	_, _ = io.WriteString(os.Stderr, "private diagnostic "+updated[0])
	return 0
}

func authHelper() int {
	dir := os.Getenv("CODEX_HOME")
	if os.Getenv("CODEX_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		return 95
	}
	record, _ := json.Marshal(map[string]any{"args": os.Args[1:], "environment": os.Environ(), "home": os.Getenv("HOME")})
	if os.WriteFile(filepath.Join(dir, "fixture-command.json"), record, 0600) != nil {
		return 96
	}
	if os.Args[1] == "logout" {
		if err := os.Remove(filepath.Join(dir, "auth.json")); err != nil && !os.IsNotExist(err) {
			return 97
		}
	} else if len(os.Args) > 2 && os.Args[2] == "--device-auth" {
		if os.WriteFile(filepath.Join(dir, "auth.json"), []byte(savedLogin), 0600) != nil {
			return 98
		}
	} else if codexauth.ValidateChatGPT(dir) != nil {
		return 99
	}
	_, _ = io.WriteString(os.Stdout, "fixture authentication completed\n")
	return 0
}

func TestSubscriptionLoginIsReusedAndRefreshPersists(t *testing.T) {
	dir := privateAuthDirectory(t)
	saveLogin(t, dir)
	t.Setenv("CODEX_API_KEY", "must-not-be-used")
	t.Setenv("OPENAI_API_KEY", "must-not-be-used")
	for attempt := 1; attempt <= 2; attempt++ {
		var stdout, stderr bytes.Buffer
		input := subscriptionInput(t, "refresh-login")
		if strings.Contains(string(input), "token") || strings.Contains(string(input), "api_key") {
			t.Fatal("subscription credentials entered request stdin")
		}
		code := runAt(t.Context(), bytes.NewReader(input), &stdout, &stderr, program(t), dir)
		if code != 0 || stderr.Len() != 0 || strings.Contains(stdout.String(), "synthetic-") || strings.Contains(stdout.String(), "must-not-be-used") {
			t.Fatalf("subscription execution failed or disclosed synthetic credential: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
		}
		decoder := json.NewDecoder(&stdout)
		for range 2 {
			var event struct {
				Home        string   `json:"home"`
				Prompt      string   `json:"prompt"`
				Args        []string `json:"args"`
				Environment []string `json:"environment"`
				Item        struct {
					Text string `json:"text"`
				} `json:"item"`
			}
			if err := decoder.Decode(&event); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(event.Item.Text, "[REDACTED]") || event.Prompt != "refresh-login" {
				t.Fatal("redacted event or stdin prompt missing")
			}
			if !contains(event.Environment, "CODEX_HOME="+dir) || len(event.Environment) != 4 || !contains(event.Args, `forced_login_method="chatgpt"`) || !contains(event.Args, `cli_auth_credentials_store="file"`) {
				t.Fatalf("subscription isolation was not configured: %+v", event)
			}
			if !strings.HasPrefix(event.Home, "/tmp/circular-codex-home-") {
				t.Fatal("subscription HOME was not private")
			}
			if _, err := os.Stat(event.Home); !os.IsNotExist(err) {
				t.Fatal("private HOME survived execution")
			}
		}
		secrets, err := codexauth.Secrets(dir)
		if err != nil || secrets[2] != "synthetic-refresh-token"+strings.Repeat("-refreshed", attempt) {
			t.Fatal("saved refresh did not survive for the next run")
		}
	}
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func TestSubscriptionRejectsMissingOrAPIKeyLoginBeforeCLI(t *testing.T) {
	for _, contents := range []string{"", `{"OPENAI_API_KEY":"synthetic-private-key"}`} {
		dir := privateAuthDirectory(t)
		if contents != "" {
			if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
		}
		var stdout, stderr bytes.Buffer
		code := runAt(t.Context(), bytes.NewReader(subscriptionInput(t, "inspect")), &stdout, &stderr, "/must-not-start", dir)
		var failure map[string]any
		if code != 1 || stderr.Len() != 0 || json.Unmarshal(stdout.Bytes(), &failure) != nil || failure["type"] != "error" || !strings.Contains(failure["message"].(string), "login") || strings.Contains(stdout.String(), "synthetic-private-key") {
			t.Fatalf("missing subscription login did not produce a safe public error: exit %d", code)
		}
	}
}

func TestExplicitModesNeverFallBack(t *testing.T) {
	for _, input := range []string{
		`{"protocol_version":1,"auth_mode":"chatgpt","prompt":"inspect","api_key":"synthetic-key"}`,
		`{"protocol_version":1,"auth_mode":"api_key","prompt":"inspect"}`,
		`{"protocol_version":1,"auth_mode":"unknown","prompt":"inspect"}`,
		`{"protocol_version":1,"auth_mode":"","prompt":"inspect"}`,
	} {
		if _, err := parse([]byte(input)); err == nil {
			t.Fatal("invalid explicit authentication mode accepted")
		}
	}
	request, err := parse([]byte(`{"protocol_version":1,"prompt":"inspect"}`))
	if err != nil || request.authMode != "chatgpt" {
		t.Fatal("omitted authentication did not default to ChatGPT")
	}
	input := strings.Replace(string(encoded(t, "inspect", "")), "{", `{"auth_mode":"api_key",`, 1)
	var stdout, stderr bytes.Buffer
	if code := runAt(t.Context(), strings.NewReader(input), &stdout, &stderr, program(t), "/missing-login"); code != 0 {
		t.Fatalf("explicit API-key mode required subscription login: %d", code)
	}
}

func TestAuthenticationCommandsUseSameDedicatedLogin(t *testing.T) {
	dir := privateAuthDirectory(t)
	for _, action := range []string{"login", "status", "logout"} {
		var stdout, stderr bytes.Buffer
		if code := authenticate(t.Context(), action, strings.NewReader(""), &stdout, &stderr, program(t), dir); code != 0 || stderr.Len() != 0 {
			t.Fatalf("%s failed: exit %d", action, code)
		}
		data, err := os.ReadFile(filepath.Join(dir, "fixture-command.json"))
		if err != nil {
			t.Fatal(err)
		}
		var record struct {
			Args        []string `json:"args"`
			Environment []string `json:"environment"`
			Home        string   `json:"home"`
		}
		if json.Unmarshal(data, &record) != nil || len(record.Environment) != 4 || !contains(record.Environment, "CODEX_HOME="+dir) {
			t.Fatal("authentication command inherited untrusted environment")
		}
		prefix := map[string][]string{"login": {"login", "--device-auth"}, "status": {"login", "status"}, "logout": {"logout"}}[action]
		want := append(prefix, "-c", `cli_auth_credentials_store="file"`, "-c", `forced_login_method="chatgpt"`)
		if !reflect.DeepEqual(record.Args, want) {
			t.Fatalf("unexpected authentication command: %v", record.Args)
		}
		if _, err := os.Stat(record.Home); !os.IsNotExist(err) {
			t.Fatal("authentication private HOME survived completion")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("logout left the saved login")
	}
	if _, err := os.Stat(filepath.Join(dir, ".circular-auth.lock")); err != nil {
		t.Fatal("authentication removed the stable lock")
	}
}

func TestSubscriptionOutputFailsClosed(t *testing.T) {
	for name, line := range map[string]string{
		"malformed": `{"text":"synthetic-id-token",`,
		"duplicate": `{"text":"synthetic-id-token","text":"second"}` + "\n",
		"Unicode":   `{"text":"\ud800"}` + "\n",
		"UTF8":      string([]byte{0xff}) + "\n",
		"oversized": strings.Repeat("x", maxOutputLine+1),
		"array":     `["synthetic-id-token"]` + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateAuthDirectory(t)
			saveLogin(t, dir)
			var stdout bytes.Buffer
			cancelled := false
			writer := &subscriptionOutput{writer: &stdout, authDir: dir, cancel: func() { cancelled = true }}
			_, _ = writer.Write([]byte(line))
			if writer.finish() == nil || !cancelled || strings.Contains(stdout.String(), "synthetic-id-token") {
				t.Fatal("invalid subscription output was forwarded")
			}
		})
	}
	dir := privateAuthDirectory(t)
	saveLogin(t, dir)
	var stdout bytes.Buffer
	writer := &subscriptionOutput{writer: &stdout, authDir: dir, cancel: func() {}}
	line := `{"nested":{"synthetic-id-token":[{"text":"synthetic-\u0061ccess-token"}]}}` + "\n"
	for _, part := range []string{line[:17], line[17:]} {
		if _, err := writer.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if writer.finish() != nil || strings.Contains(stdout.String(), "synthetic-") || !strings.Contains(stdout.String(), "[REDACTED]") {
		t.Fatal("escaped or nested credentials were not redacted")
	}
	if err := os.Remove(filepath.Join(dir, "auth.json")); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if _, err := writer.Write([]byte(line)); err == nil || strings.Contains(stdout.String(), "synthetic-") {
		t.Fatal("output was forwarded after login disappeared")
	}
}

func TestSubscriptionRunWaitsForAuthenticationLock(t *testing.T) {
	dir := privateAuthDirectory(t)
	saveLogin(t, dir)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := codexauth.WithLock(t.Context(), dir, func() error {
		cancel()
		var stdout, stderr bytes.Buffer
		if code := runAt(ctx, bytes.NewReader(subscriptionInput(t, "inspect")), &stdout, &stderr, "/must-not-start", dir); code != 1 || stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatal("cancelled subscription execution produced output or started CLI")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func privateAuthDirectory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
