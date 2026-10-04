package execution

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
)

// validateCodexAuthRoot checks paths only: preflight must neither create a login
// directory nor read credentials. Worker- and daemon-visible roots can differ.
func validateCodexAuthRoot(config Config) error {
	bad := errors.New("Codex auth root must be a dedicated non-symlink directory outside repositories, worktrees and artifacts")
	root := config.CodexAuthRoot
	if !filepath.IsAbs(root) || !utf8.ValidString(root) || strings.ContainsAny(root, "\x00,\n\r") || filepath.Clean(root) == "/" {
		return bad
	}
	resolved, err := resolve(filepath.Clean(root))
	if err != nil || resolved != filepath.Clean(root) {
		return bad
	}
	root = resolved
	if info, err := os.Lstat(root); err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !info.IsDir() {
		return bad
	}
	for _, other := range []string{config.Git.RepositoryCacheRoot, config.Git.WorktreeRoot, config.ArtifactRoot, config.ReviewContextRoot} {
		if other == "" {
			continue
		}
		if !filepath.IsAbs(other) {
			return bad
		}
		other, err = resolve(filepath.Clean(other))
		if err != nil || root == other || strings.HasPrefix(root, other+"/") || strings.HasPrefix(other, root+"/") {
			return bad
		}
	}
	return nil
}

// PrepareCodexAuth creates only Circular's dedicated directory, owned by the
// runner. Existing directories must already be private and correctly owned;
// never recursively chmod/chown or read an existing account's credentials.
func PrepareCodexAuth(config Config) error {
	if err := validateCodexAuthRoot(config); err != nil {
		return err
	}
	parts := strings.Split(config.Docker.ContainerUser, ":")
	if len(parts) != 2 {
		return fmt.Errorf("Codex auth requires a numeric non-root runner UID:GID")
	}
	uid, errUID := strconv.Atoi(parts[0])
	gid, errGID := strconv.Atoi(parts[1])
	if errUID != nil || errGID != nil || uid <= 0 || gid <= 0 {
		return fmt.Errorf("Codex auth requires a numeric non-root runner UID:GID")
	}
	root := config.CodexAuthRoot
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		return fmt.Errorf("cannot create parent directory for Circular Codex authentication")
	}
	if err := os.Mkdir(root, 0700); err == nil {
		if os.Getuid() != uid || os.Getgid() != gid {
			if err := os.Chown(root, uid, gid); err != nil {
				return fmt.Errorf("cannot assign Circular Codex auth directory to the runner user")
			}
		}
	} else if !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("cannot create Circular Codex auth directory")
	}
	if err := validateCodexAuthRoot(config); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("cannot inspect Circular Codex auth directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != 0700 || int(stat.Uid) != uid || int(stat.Gid) != gid {
		return fmt.Errorf("Circular Codex auth directory must have mode 0700 and belong to the configured runner UID:GID")
	}
	return nil
}
