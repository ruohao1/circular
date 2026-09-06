// Package git owns managed Repository checkouts, Run worktrees, and final diffs.
// Callers retain responsibility for durable Run leases and resource handoff.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrConfiguration = errors.New("invalid managed Git configuration")
	ErrInvalidCache  = errors.New("not a managed Repository checkout")
	ErrClone         = errors.New("Repository clone failed")
	ErrFetch         = errors.New("Repository refresh failed")
	ErrLock          = errors.New("managed Git lock failed")
	ErrCleanup       = errors.New("owned Git allocation cleanup failed")
)

// Error preserves operation identity and compensation failures without retaining
// Git stderr, clone URLs, ref text, or command arguments in diagnostics.
type Error struct {
	Kind         error
	ID           uuid.UUID
	Path         string
	ExitCode     *int
	Cause        error
	CleanupError error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%v for %s at %s", e.Kind, e.ID, e.Path)
}
func (e *Error) Unwrap() []error {
	var causes []error
	for _, cause := range []error{e.Kind, e.Cause, e.CleanupError} {
		if cause != nil {
			causes = append(causes, cause)
		}
	}
	return causes
}

func failure(kind error, id uuid.UUID, path string, code int, cause error) error {
	e := &Error{Kind: kind, ID: id, Path: path, Cause: cause}
	if code >= 0 {
		e.ExitCode = &code
	}
	return e
}

type Config struct {
	RepositoryCacheRoot string
	WorktreeRoot        string
	GitExecutable       string
	LockTimeout         time.Duration
	Owner               *FileOwner
}

type FileOwner struct{ UID, GID int }

// Local shares the existing Python lock paths and on-disk resource identities.
// It has no process-local ownership registry; crash recovery uses durable proof.
type Local struct{ config Config }

func NewLocal(config Config) (*Local, error) {
	var err error
	config.RepositoryCacheRoot, err = canonicalRoot(config.RepositoryCacheRoot)
	if err != nil {
		return nil, ErrConfiguration
	}
	config.WorktreeRoot, err = canonicalRoot(config.WorktreeRoot)
	if err != nil {
		return nil, ErrConfiguration
	}
	a, b := config.RepositoryCacheRoot, config.WorktreeRoot
	if a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator)) {
		return nil, ErrConfiguration
	}
	if config.GitExecutable == "" {
		config.GitExecutable = "git"
	}
	if !safeText(config.GitExecutable) {
		return nil, ErrConfiguration
	}
	if config.LockTimeout == 0 {
		config.LockTimeout = 30 * time.Second
	}
	if config.LockTimeout < 0 {
		return nil, ErrConfiguration
	}
	if config.Owner != nil {
		owner := *config.Owner
		if owner.UID < 0 || owner.GID < 0 || uint64(owner.UID) > (1<<32)-2 || uint64(owner.GID) > (1<<32)-2 {
			return nil, ErrConfiguration
		}
		config.Owner = &owner
	}
	return &Local{config: config}, nil
}

func safeText(s string) bool  { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }
func exists(path string) bool { _, err := os.Lstat(path); return !errors.Is(err, os.ErrNotExist) }

func resolveMissing(path string) (string, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return filepath.EvalSymlinks(path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	resolved, err := resolveMissing(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}

func canonicalRoot(path string) (string, error) {
	if !filepath.IsAbs(path) || !safeText(path) {
		return "", ErrConfiguration
	}
	path = filepath.Clean(path)
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", ErrConfiguration
	}
	resolved, err := resolveMissing(path)
	if err != nil || resolved == string(filepath.Separator) {
		return "", ErrConfiguration
	}
	return resolved, nil
}

func canonical(path string) bool {
	resolved, err := resolveMissing(path)
	return err == nil && resolved == path
}

func ensureRoot(path string) error {
	if !canonical(path) {
		return ErrConfiguration
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if !canonical(path) {
		return ErrConfiguration
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return ErrConfiguration
	}
	return nil
}

func (l *Local) lock(ctx context.Context, id uuid.UUID, path string) (func() error, error) {
	file, err := os.OpenFile(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, failure(ErrLock, id, path, -1, nil)
	}
	locked := false
	defer func() {
		if !locked {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, failure(ErrLock, id, path, -1, nil)
	}
	wait, cancel := context.WithTimeout(ctx, l.config.LockTimeout)
	defer cancel()
	for {
		if err := wait.Err(); err != nil {
			return nil, failure(ErrLock, id, path, -1, err)
		}
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return nil, failure(ErrLock, id, path, -1, nil)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-wait.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	locked = true
	return func() error {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return failure(ErrLock, id, path, -1, nil)
		}
		return nil
	}, nil
}

func (l *Local) run(ctx context.Context, environment map[string]string, args ...string) ([]byte, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, -1, err
	}
	flags := []string{"-c", "protocol.allow=never", "-c", "protocol.file.allow=always", "-c", "protocol.https.allow=always", "-c", "core.hooksPath=/dev/null"}
	for i, arg := range args {
		if arg == "-C" && i+1 < len(args) {
			flags = append(flags, "-c", "safe.directory="+args[i+1])
			break
		}
	}
	cmd := exec.Command(l.config.GitExecutable, append(flags, args...)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := map[string]string{}
	for _, entry := range os.Environ() {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE":
			continue
		}
		env[name] = value
	}
	env["GIT_ALLOW_PROTOCOL"], env["GIT_TERMINAL_PROMPT"], env["GCM_INTERACTIVE"] = "file:https", "0", "Never"
	for name, value := range environment {
		env[name] = value
	}
	for name, value := range env {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &output, io.Discard, time.Second
	if err := cmd.Start(); err != nil {
		return nil, -1, errors.New("Git executable is unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(time.Second)
		select {
		case <-done:
			timer.Stop()
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		// Git helpers belong to this process group as well. Do not let an
		// orphan continue mutating resources after its metadata lock is freed.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil, -1, ctx.Err()
	}
	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return nil, -1, errors.New("Git output could not be fully read")
	}
	return output.Bytes(), cmd.ProcessState.ExitCode(), nil
}
