package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

var ErrBase = errors.New("Run base commit could not be verified")

// runBase lives in the trusted cache, outside the agent's mounted worktree.
// The ref also retains its object if the Run branch advances or is removed.
type runBase struct {
	RunID        uuid.UUID `json:"run_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
	Commit       string    `json:"commit"`
	Ref          string    `json:"ref"`
}

func baseRef(runID uuid.UUID) string { return "refs/circular/bases/" + runID.String() }

func (l *Local) pinBase(ctx context.Context, repository string, base runBase) (result error) {
	root, err := os.OpenRoot(filepath.Join(repository, ".git"))
	if err != nil {
		return ErrBase
	}
	defer root.Close()
	if err := root.Mkdir("circular-bases", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return ErrBase
	}
	info, err := root.Lstat("circular-bases")
	if err != nil || !info.IsDir() {
		return ErrBase
	}
	pins, err := root.OpenRoot("circular-bases")
	if err != nil {
		return ErrBase
	}
	defer pins.Close()
	name := base.RunID.String() + ".json"
	payload, err := json.Marshal(base)
	if err != nil || len(payload) > 4096 {
		return ErrBase
	}
	// A previous allocation is never allowed to select a new base for this Run.
	if _, err := pins.Lstat(name); err == nil || !errors.Is(err, os.ErrNotExist) {
		return ErrBase
	}
	temporary := "." + uuid.NewString()
	file, err := pins.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrBase
	}
	defer func() {
		_ = file.Close()
		if err := pins.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, ErrBase)
		}
	}()
	if _, err := file.Write(payload); err != nil {
		return ErrBase
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return ErrBase
	}
	if err := pins.Link(temporary, name); err != nil {
		return ErrBase
	}
	complete := false
	defer func() {
		if !complete {
			_ = pins.Remove(name)
		}
	}()
	if err := errors.Join(syncRoot(pins), syncRoot(root)); err != nil {
		return ErrBase
	}
	_, code, err := l.run(ctx, nil, "-C", repository, "update-ref", baseRef(base.RunID), base.Commit, strings.Repeat("0", len(base.Commit)))
	if err != nil || code != 0 {
		return ErrBase
	}
	complete = true
	return nil
}

func (l *Local) readBase(ctx context.Context, repository string, repositoryID, runID uuid.UUID) (runBase, error) {
	root, err := os.OpenRoot(filepath.Join(repository, ".git"))
	if err != nil {
		return runBase{}, ErrBase
	}
	defer root.Close()
	name := "circular-bases/" + runID.String() + ".json"
	// Reject a symlinked parent, including links that stay within the cache.
	info, err := root.Lstat("circular-bases")
	if err != nil && !errors.Is(err, os.ErrNotExist) || err == nil && !info.IsDir() {
		return runBase{}, ErrBase
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		// Older workers did not save a pin. Their container could not access the
		// managed cache, and cleanup deliberately retained this exact Run ref.
		// A partial new pin is not a legacy record and must never fall back.
		_, code, checkErr := l.run(ctx, nil, "-C", repository, "show-ref", "--verify", "--quiet", baseRef(runID))
		if checkErr != nil || code != 1 {
			return runBase{}, ErrBase
		}
		commit, err := l.branchCommit(ctx, repository, "circular/run/"+runID.String())
		if err != nil {
			return runBase{}, ErrBase
		}
		return runBase{RunID: runID, RepositoryID: repositoryID, Commit: string(commit)}, nil
	}
	if err != nil {
		return runBase{}, ErrBase
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return runBase{}, ErrBase
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return runBase{}, ErrBase
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var base runBase
	if err := decoder.Decode(&base); err != nil || base.RunID != runID || base.RepositoryID != repositoryID || !commitPattern.MatchString(base.Commit) || base.Ref == "" || !safeText(base.Ref) || strings.ContainsAny(base.Ref, "\r\n") {
		return runBase{}, ErrBase
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return runBase{}, ErrBase
	}
	ref, code, err := l.run(ctx, nil, "-C", repository, "show-ref", "--verify", "--hash", baseRef(runID))
	if err != nil || code != 0 || strings.TrimSpace(string(ref)) != base.Commit {
		return runBase{}, ErrBase
	}
	return base, nil
}
