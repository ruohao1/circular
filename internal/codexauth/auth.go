// Package codexauth coordinates access to Circular's dedicated Codex login.
// Callers supply an explicit directory; this package never searches user homes.
package codexauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	ErrDirectory = errors.New("Codex login directory must be an owned private directory with mode 0700")
	ErrLock      = errors.New("Codex login lock must be an owned private regular file with mode 0600")
	ErrLogin     = errors.New("Codex ChatGPT login required; run the Circular Codex login command")
)

const maxAuthBytes = 1024 * 1024

func directory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrDirectory
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrDirectory
	}
	file := os.NewFile(uintptr(fd), path)
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&0777 != 0700 || stat.Uid != uint32(os.Geteuid()) {
		file.Close()
		return nil, ErrDirectory
	}
	return file, nil
}

func privateFile(dir *os.File, name string, flags int, invalid error) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, invalid
	}
	file := os.NewFile(uintptr(fd), name)
	var stat syscall.Stat_t
	if syscall.Fstat(fd, &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		file.Close()
		return nil, invalid
	}
	return file, nil
}

// WithLock serializes login, logout, refresh, and Runs across processes. The
// stable lock file is never removed, including after cancellation or a crash.
func WithLock(ctx context.Context, path string, action func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := directory(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	lock, err := privateFile(dir, ".circular-auth.lock", syscall.O_RDWR|syscall.O_CREAT, ErrLock)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			if err := ctx.Err(); err != nil {
				return err
			}
			return action()
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return ErrLock
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type login struct {
	AuthMode string  `json:"auth_mode"`
	APIKey   *string `json:"OPENAI_API_KEY"`
	Tokens   struct {
		ID      string `json:"id_token"`
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	} `json:"tokens"`
}

// Secrets returns only the known authentication tokens. Call while holding
// WithLock; repeat after CLI output to observe credentials refreshed on disk.
func Secrets(path string) ([]string, error) {
	dir, err := directory(path)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	file, err := privateFile(dir, "auth.json", syscall.O_RDONLY|syscall.O_NONBLOCK, ErrLogin)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAuthBytes+1))
	if err != nil || len(data) > maxAuthBytes {
		return nil, ErrLogin
	}
	var value login
	if json.Unmarshal(data, &value) != nil || value.AuthMode != "" && value.AuthMode != "chatgpt" || value.APIKey != nil && *value.APIKey != "" {
		return nil, ErrLogin
	}
	secrets := []string{value.Tokens.ID, value.Tokens.Access, value.Tokens.Refresh}
	for _, secret := range secrets {
		if strings.TrimSpace(secret) == "" {
			return nil, ErrLogin
		}
	}
	return secrets, nil
}

func ValidateChatGPT(path string) error {
	_, err := Secrets(path)
	return err
}
