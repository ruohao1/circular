package codexauth

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const syntheticLogin = `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"synthetic-id","access_token":"synthetic-access","refresh_token":"synthetic-refresh","account_id":"account"},"last_refresh":"2026-09-12T00:00:00Z"}`

func TestSavedChatGPTLogin(t *testing.T) {
	dir := privateAuthDirectory(t)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(syntheticLogin), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(t.Context(), dir, func() error {
		if err := ValidateChatGPT(dir); err != nil {
			return err
		}
		secrets, err := Secrets(dir)
		if err == nil && !reflect.DeepEqual(secrets, []string{"synthetic-id", "synthetic-access", "synthetic-refresh"}) {
			t.Fatal("known token fields were not read")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lock, err := os.Stat(filepath.Join(dir, ".circular-auth.lock"))
	if err != nil || lock.Mode().Perm() != 0600 {
		t.Fatalf("stable private lock missing: %v", err)
	}
}

func TestRejectsMissingOrWrongAuthentication(t *testing.T) {
	for name, data := range map[string]string{
		"missing": "", "malformed": `{`, "API key": `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-key"}`,
		"both modes":    strings.Replace(syntheticLogin, `"OPENAI_API_KEY":null`, `"OPENAI_API_KEY":"synthetic-key"`, 1),
		"wrong mode":    strings.Replace(syntheticLogin, `"chatgpt"`, `"api"`, 1),
		"missing token": strings.Replace(syntheticLogin, `"synthetic-refresh"`, `""`, 1),
		"oversized":     strings.Repeat(" ", maxAuthBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := privateAuthDirectory(t)
			if data != "" {
				if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := ValidateChatGPT(dir); !errors.Is(err, ErrLogin) {
				t.Fatalf("invalid login accepted: %v", err)
			}
		})
	}
}

func TestRejectsUnsafeCredentialPaths(t *testing.T) {
	dir := privateAuthDirectory(t)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(syntheticLogin), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateChatGPT(dir); !errors.Is(err, ErrLogin) {
		t.Fatal("readable credential file accepted")
	}
	link := filepath.Join(t.TempDir(), "login")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(t.Context(), link, func() error { t.Fatal("symlink action ran"); return nil }); !errors.Is(err, ErrDirectory) {
		t.Fatalf("symlink directory accepted: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "auth.json"), filepath.Join(dir, ".circular-auth.lock")); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(t.Context(), dir, func() error { t.Fatal("symlink lock action ran"); return nil }); !errors.Is(err, ErrLock) {
		t.Fatalf("symlink lock accepted: %v", err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateChatGPT(dir); !errors.Is(err, ErrDirectory) {
		t.Fatal("public directory accepted")
	}
}

func TestLockProcessHelper(t *testing.T) {
	dir := os.Getenv("CIRCULAR_AUTH_LOCK_HELPER")
	if dir == "" {
		return
	}
	if err := WithLock(context.Background(), dir, func() error {
		_, _ = io.WriteString(os.Stdout, "locked\n")
		_, err := io.Copy(io.Discard, os.Stdin)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLockSerializesProcessesAndWaitCanBeCancelled(t *testing.T) {
	dir := privateAuthDirectory(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), program, "-test.run=^TestLockProcessHelper$")
	child.Env = append(os.Environ(), "CIRCULAR_AUTH_LOCK_HELPER="+dir)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stdin.Close(); _ = child.Process.Kill(); _ = child.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("child did not acquire lock: %q %v", line, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := WithLock(ctx, dir, func() error { t.Fatal("concurrent access entered"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock waiter did not cancel: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := WithLock(t.Context(), dir, func() error { return nil }); err != nil {
		t.Fatalf("lock was not released: %v", err)
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
