// Package artifacts stores immutable Run output outside container worktrees.
package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrContent   = errors.New("artifact content could not be safely stored or retrieved")
	ErrImmutable = errors.New("artifact content is immutable")
	artifactName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

type Content struct {
	URI       string
	SizeBytes int64
	SHA256    string
}

type LocalStore struct{ root string }

func NewLocalStore(root string) (*LocalStore, error) {
	if !filepath.IsAbs(root) || !utf8.ValidString(root) || strings.ContainsRune(root, 0) {
		return nil, ErrContent
	}
	root, err := resolve(filepath.Clean(root))
	if err != nil || root == string(filepath.Separator) {
		return nil, ErrContent
	}
	return &LocalStore{root: root}, nil
}

func URI(runID uuid.UUID, name string) (string, error) {
	if !artifactName.MatchString(name) {
		return "", ErrContent
	}
	return "artifact://" + runID.String() + "/" + name, nil
}

func (s *LocalStore) Write(ctx context.Context, runID uuid.UUID, name string, content []byte) (Content, error) {
	return s.WriteStream(ctx, runID, name, bytes.NewReader(content))
}

// WriteStream uses bounded reads and atomically publishes complete bytes without
// replacing an existing URI. Identical retries succeed; differing bytes fail.
// The reader must honor its own I/O deadlines; context is checked between reads.
func (s *LocalStore) WriteStream(ctx context.Context, runID uuid.UUID, name string, content io.Reader) (stored Content, result error) {
	uri, err := URI(runID, name)
	if err != nil || content == nil {
		return Content{}, ErrContent
	}
	if err := ctx.Err(); err != nil {
		return Content{}, err
	}
	run, err := s.openRun(runID, true)
	if err != nil {
		return Content{}, ErrContent
	}
	defer run.Close()
	temporary := "." + name + "." + uuid.NewString()
	file, err := run.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return Content{}, ErrContent
	}
	defer func() {
		_ = file.Close()
		if err := run.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, ErrContent)
		}
	}()
	digest := sha256.New()
	size, err := io.CopyBuffer(io.MultiWriter(file, digest), contextReader{ctx, content}, make([]byte, 1024*1024))
	if err != nil {
		return Content{}, errors.Join(ErrContent, ctx.Err())
	}
	if err := file.Chmod(0600); err != nil {
		return Content{}, ErrContent
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return Content{}, ErrContent
	}
	if err := ctx.Err(); err != nil {
		return Content{}, err
	}
	if err := run.Link(temporary, name); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return Content{}, ErrContent
		}
		existing, err := openContent(run, name)
		if err != nil {
			return Content{}, ErrContent
		}
		existingDigest := sha256.New()
		existingSize, err := io.CopyBuffer(existingDigest, contextReader{ctx, existing}, make([]byte, 1024*1024))
		closeErr := existing.Close()
		if err != nil || closeErr != nil {
			return Content{}, errors.Join(ErrContent, ctx.Err())
		}
		if existingSize != size || !bytes.Equal(existingDigest.Sum(nil), digest.Sum(nil)) {
			return Content{}, errors.Join(ErrContent, ErrImmutable)
		}
	}
	// Even identical retries sync all directory entries: a previous process may
	// have published bytes then crashed before syncing a newly created ancestor.
	if err := syncDirectory(run); err != nil {
		return Content{}, ErrContent
	}
	for directory := s.root; ; directory = filepath.Dir(directory) {
		file, err := os.OpenFile(directory, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return Content{}, ErrContent
		}
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return Content{}, ErrContent
		}
		if directory == filepath.Dir(directory) {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return Content{}, err
	}
	return Content{URI: uri, SizeBytes: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func (s *LocalStore) Read(ctx context.Context, runID uuid.UUID, uri string) ([]byte, error) {
	prefix := "artifact://" + runID.String() + "/"
	if !strings.HasPrefix(uri, prefix) {
		return nil, ErrContent
	}
	name := strings.TrimPrefix(uri, prefix)
	if expected, err := URI(runID, name); err != nil || expected != uri {
		return nil, ErrContent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run, err := s.openRun(runID, false)
	if err != nil {
		return nil, ErrContent
	}
	defer run.Close()
	file, err := openContent(run, name)
	if err != nil {
		return nil, ErrContent
	}
	defer file.Close()
	data, err := io.ReadAll(contextReader{ctx, file})
	if err != nil {
		return nil, errors.Join(ErrContent, ctx.Err())
	}
	return data, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func openContent(root *os.Root, name string) (*os.File, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, ErrContent
	}
	return file, nil
}

func (s *LocalStore) openRun(runID uuid.UUID, create bool) (*os.Root, error) {
	resolved, err := resolve(s.root)
	if err != nil || resolved != s.root {
		return nil, ErrContent
	}
	if create {
		if err := os.MkdirAll(s.root, 0700); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := runID.String()
	if create {
		if err := root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	run, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := run.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		run.Close()
		return nil, ErrContent
	}
	return run, nil
}

func resolve(path string) (string, error) {
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
	resolved, err := resolve(parent)
	return filepath.Join(resolved, filepath.Base(path)), err
}

func syncDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}
