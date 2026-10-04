package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrDelivery            = errors.New("saved Run changes could not be prepared for delivery")
	ErrDeliveryUnsupported = errors.New("saved Run contains changes that cannot be published automatically")
	ErrDeliverySize        = errors.New("saved Run exceeds automatic delivery limits")
)

const (
	MaxDeliveryPatchBytes = 32 * 1024 * 1024
	MaxDeliveryBlobBytes  = 20 * 1024 * 1024
	MaxDeliveryTotalBytes = 64 * 1024 * 1024
	MaxDeliveryFiles      = 1000
)

type DeliveryFile struct {
	Path, Mode, SHA string
	Content         []byte
	Delete          bool
}

type DeliveryChanges struct {
	BaseCommit, BaseTree, BaseRef, TreeSHA string
	Files                                  []DeliveryFile
}

// PrepareDelivery reconstructs only the caller's verified immutable patch. It
// never reads archived worktree files, refreshes a remote, modifies the cache's
// objects/index/refs, or runs repository hooks. The pinned base (or an exact
// retained legacy Run branch) is the sole source of historical identity.
func (l *Local) PrepareDelivery(ctx context.Context, repositoryID, runID uuid.UUID, patch []byte, baseCommit string) (DeliveryChanges, error) {
	if repositoryID == uuid.Nil || runID == uuid.Nil || baseCommit != "" && !commitPattern.MatchString(baseCommit) {
		return DeliveryChanges{}, ErrDelivery
	}
	if len(patch) > MaxDeliveryPatchBytes {
		return DeliveryChanges{}, ErrDeliverySize
	}
	repository := filepath.Join(l.config.RepositoryCacheRoot, repositoryID.String())
	if id, err := l.repositoryID(repository); err != nil || id != repositoryID {
		return DeliveryChanges{}, ErrDelivery
	}
	if err := l.validateCache(ctx, repositoryID, repository); err != nil {
		return DeliveryChanges{}, errors.Join(ErrDelivery, err)
	}
	base, err := l.readBase(ctx, repository, repositoryID, runID)
	if err != nil || baseCommit != "" && base.Commit != strings.ToLower(baseCommit) {
		return DeliveryChanges{}, errors.Join(ErrDelivery, ErrBase)
	}
	// GitHub's Git database API uses SHA-1 object IDs.
	if len(base.Commit) != 40 {
		return DeliveryChanges{}, ErrDeliveryUnsupported
	}
	objects := filepath.Join(repository, ".git", "objects")
	if info, err := os.Lstat(objects); err != nil || !info.IsDir() || !canonical(objects) {
		return DeliveryChanges{}, ErrDelivery
	}
	scratch, err := os.MkdirTemp("", "circular-delivery-")
	if err != nil {
		return DeliveryChanges{}, ErrDelivery
	}
	defer os.RemoveAll(scratch)
	directory := filepath.Join(scratch, "repository.git")
	environment := map[string]string{
		"GIT_DIR": directory, "GIT_COMMON_DIR": directory,
		"GIT_INDEX_FILE":                   filepath.Join(scratch, "index"),
		"GIT_OBJECT_DIRECTORY":             filepath.Join(directory, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": strconv.Quote(objects),
		"GIT_CONFIG_NOSYSTEM":              "1", "GIT_CONFIG_SYSTEM": os.DevNull,
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_COUNT": "0",
		"GIT_ATTR_NOSYSTEM": "1", "GIT_NO_REPLACE_OBJECTS": "1",
	}
	run := func(args ...string) ([]byte, error) {
		output, code, err := l.run(ctx, environment, args...)
		if err != nil || code != 0 {
			return nil, errors.Join(ErrDelivery, err)
		}
		return output, nil
	}
	if _, err := run("init", "--bare", "--template=", "--object-format=sha1", directory); err != nil {
		return DeliveryChanges{}, err
	}
	output, err := run("cat-file", "-t", base.Commit)
	if err != nil || strings.TrimSpace(string(output)) != "commit" {
		return DeliveryChanges{}, errors.Join(ErrDelivery, ErrBase)
	}
	output, err = run("rev-parse", "--verify", "--end-of-options", base.Commit+"^{tree}")
	if err != nil {
		return DeliveryChanges{}, err
	}
	baseTree := strings.TrimSpace(string(output))
	if len(baseTree) != 40 || !commitPattern.MatchString(baseTree) {
		return DeliveryChanges{}, ErrDelivery
	}
	if _, err := run("read-tree", base.Commit); err != nil {
		return DeliveryChanges{}, err
	}
	if len(patch) != 0 {
		patchFile := filepath.Join(scratch, "changes.patch")
		if err := os.WriteFile(patchFile, patch, 0600); err != nil {
			return DeliveryChanges{}, ErrDelivery
		}
		if _, err := run("apply", "--cached", "--binary", "--whitespace=nowarn", "--", patchFile); err != nil {
			return DeliveryChanges{}, err
		}
	}
	output, err = run("write-tree")
	if err != nil {
		return DeliveryChanges{}, err
	}
	tree := strings.TrimSpace(string(output))
	if len(tree) != 40 || !commitPattern.MatchString(tree) {
		return DeliveryChanges{}, ErrDelivery
	}
	output, err = run("diff-tree", "--raw", "--no-renames", "--no-ext-diff", "--no-textconv", "--no-commit-id", "-r", "-z", base.Commit, tree, "--")
	if err != nil {
		return DeliveryChanges{}, err
	}
	files, err := deliveryFiles(output)
	if err != nil {
		return DeliveryChanges{}, err
	}
	total := int64(0)
	for i := range files {
		file := &files[i]
		if file.Delete {
			continue
		}
		output, err := run("cat-file", "-s", file.SHA)
		if err != nil {
			return DeliveryChanges{}, err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
		if err != nil || size < 0 {
			return DeliveryChanges{}, ErrDelivery
		}
		total += size
		if size > MaxDeliveryBlobBytes || total > MaxDeliveryTotalBytes {
			return DeliveryChanges{}, ErrDeliverySize
		}
		file.Content, err = run("cat-file", "blob", file.SHA)
		if err != nil || int64(len(file.Content)) != size {
			return DeliveryChanges{}, ErrDelivery
		}
	}
	return DeliveryChanges{BaseCommit: base.Commit, BaseTree: baseTree, BaseRef: base.Ref, TreeSHA: tree, Files: files}, nil
}

func deliveryFiles(raw []byte) ([]DeliveryFile, error) {
	files := []DeliveryFile{}
	if len(raw) == 0 {
		return files, nil
	}
	records := bytes.Split(raw, []byte{0})
	if len(records)%2 != 1 || len(records[len(records)-1]) != 0 {
		return nil, ErrDelivery
	}
	if len(records)/2 > MaxDeliveryFiles {
		return nil, ErrDeliverySize
	}
	for i := 0; i+1 < len(records); i += 2 {
		fields := strings.Fields(string(records[i]))
		name := string(records[i+1])
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") || !deliveryPath(name) || len(fields[2]) != 40 || !commitPattern.MatchString(fields[2]) || len(fields[3]) != 40 || !commitPattern.MatchString(fields[3]) {
			return nil, ErrDelivery
		}
		oldMode, newMode := strings.TrimPrefix(fields[0], ":"), fields[1]
		if oldMode == "160000" || newMode == "160000" || strings.HasPrefix(strings.ToLower(name), ".github/workflows/") {
			return nil, ErrDeliveryUnsupported
		}
		deleted := newMode == "000000"
		mode, sha := newMode, fields[3]
		if deleted {
			mode, sha = oldMode, ""
		}
		if mode != "100644" && mode != "100755" && mode != "120000" {
			return nil, ErrDeliveryUnsupported
		}
		if fields[4] != "A" && fields[4] != "M" && fields[4] != "D" && fields[4] != "T" {
			return nil, ErrDelivery
		}
		files = append(files, DeliveryFile{Path: name, Mode: mode, SHA: sha, Delete: deleted})
	}
	return files, nil
}

func deliveryPath(name string) bool {
	if name == "" || len(name) > 4096 || !safeText(name) || path.IsAbs(name) || path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}
