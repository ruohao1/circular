package prreviews

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
)

func ValidateContext(value Context) error {
	if value.RunID == uuid.Nil || value.ReviewID == uuid.Nil || value.Snapshot.SourceRunID == uuid.Nil || value.Snapshot.TaskID == uuid.Nil || value.Snapshot.ProjectID == uuid.Nil || value.Snapshot.RepositoryID == uuid.Nil || value.RunID == value.Snapshot.SourceRunID || !CommitPattern.MatchString(value.MergeBaseSHA) || !DigestPattern.MatchString(value.DiffSHA256) || len(value.Files) > MaxFiles {
		return ErrSourceInvalid
	}
	sealed, err := SealSnapshot(value.Snapshot)
	if err != nil || sealed.InputFingerprint != value.Snapshot.InputFingerprint {
		return ErrSourceInvalid
	}
	if !CommitPattern.MatchString(value.Snapshot.PR.HeadSHA) || !CommitPattern.MatchString(value.Snapshot.PR.BaseSHA) {
		return ErrSourceInvalid
	}
	for _, f := range value.Files {
		if f.OldPath == "" && f.NewPath == "" || f.OldPath != "" && !SafePath(f.OldPath) || f.NewPath != "" && !SafePath(f.NewPath) || f.BaseLines < 0 || f.HeadLines < 0 {
			return ErrSourceInvalid
		}
		switch f.Status {
		case "added", "deleted", "modified", "renamed":
		default:
			return ErrSourceInvalid
		}
		for _, side := range []struct {
			ranges []LineRange
			count  int
		}{{f.BaseChanged, f.BaseLines}, {f.HeadChanged, f.HeadLines}} {
			for _, r := range side.ranges {
				if r.Start < 1 || r.End < r.Start || r.End > side.count {
					return ErrSourceInvalid
				}
			}
		}
	}
	return nil
}
func openContextDirectory(directory string) (*os.Root, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, ErrSourceInvalid
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return nil, ErrSourceInvalid
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return nil, ErrSourceInvalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, ErrSourceInvalid
	}
	return root, nil
}
func readContextFile(root *os.Root, name string, limit int) ([]byte, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrSourceInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, ErrSourceInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, ErrSourceInvalid
	}
	return data, nil
}
func ReadBundle(directory string) (Context, []byte, error) {
	var value Context
	root, err := openContextDirectory(directory)
	if err != nil {
		return value, nil, err
	}
	defer root.Close()
	raw, err := readContextFile(root, "context.json", MaxContextBytes)
	if err != nil {
		return value, nil, err
	}
	if err := DecodeStrict(raw, &value); err != nil {
		return value, nil, ErrSourceInvalid
	}
	if err := ValidateContext(value); err != nil {
		return value, nil, err
	}
	diff, err := readContextFile(root, "diff.patch", MaxDiffBytes)
	if err != nil {
		return value, nil, err
	}
	sum := sha256.Sum256(diff)
	if hex.EncodeToString(sum[:]) != value.DiffSHA256 {
		return value, nil, ErrSourceInvalid
	}
	return value, diff, nil
}
func ReadContext(directory string) (Context, error) {
	value, _, err := ReadBundle(directory)
	return value, err
}

// WriteContext publishes one complete worker-owned bundle. Existing identity is
// immutable, including across an interrupted database acknowledgement.
func WriteContext(directory string, value Context, diff []byte) (result error) {
	if err := ValidateContext(value); err != nil {
		return err
	}
	raw, err := CanonicalJSON(value)
	if err != nil || len(raw) > MaxContextBytes || len(diff) > MaxDiffBytes {
		return ErrSourceInvalid
	}
	sum := sha256.Sum256(diff)
	if hex.EncodeToString(sum[:]) != value.DiffSHA256 {
		return ErrSourceInvalid
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || filepath.Base(directory) != value.RunID.String() {
		return ErrSourceInvalid
	}
	parent := filepath.Dir(directory)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return ErrSourceInvalid
	}
	root, err := openContextDirectory(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	verify := func() error {
		saved, existing, err := ReadBundle(directory)
		if err != nil {
			return err
		}
		want, _ := Fingerprint(value)
		got, _ := Fingerprint(saved)
		if want != got || !bytes.Equal(existing, diff) {
			return ErrSourceInvalid
		}
		return nil
	}
	if _, err := root.Lstat(value.RunID.String()); err == nil {
		return verify()
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrSourceInvalid
	}
	staging := "." + value.RunID.String() + "." + uuid.NewString()
	if err := root.Mkdir(staging, 0700); err != nil {
		return ErrSourceInvalid
	}
	defer func() { _ = root.Chmod(staging, 0700); _ = root.RemoveAll(staging) }()
	temp, err := root.OpenRoot(staging)
	if err != nil {
		return ErrSourceInvalid
	}
	defer temp.Close()
	for name, content := range map[string][]byte{"context.json": raw, "diff.patch": diff} {
		file, err := temp.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0444)
		if err != nil {
			return ErrSourceInvalid
		}
		_, writeErr := file.Write(content)
		syncErr := file.Sync()
		closeErr := file.Close()
		if errors.Join(writeErr, syncErr, closeErr) != nil {
			return ErrSourceInvalid
		}
	}
	if err := temp.Chmod(".", 0755); err != nil {
		return ErrSourceInvalid
	}
	dir, err := temp.Open(".")
	if err != nil {
		return ErrSourceInvalid
	}
	err = errors.Join(dir.Sync(), dir.Close())
	if err != nil {
		return ErrSourceInvalid
	}
	if err := root.Rename(staging, value.RunID.String()); err != nil {
		return verify()
	}
	dir, err = root.Open(".")
	if err != nil {
		return ErrSourceInvalid
	}
	if err = errors.Join(dir.Sync(), dir.Close()); err != nil {
		return ErrSourceInvalid
	}
	return verify()
}
