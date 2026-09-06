package artifacts_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
)

func TestContentPublicationIsImmutableAndReadableAcrossStoreInstances(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := artifacts.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.MustParse("00000000-0000-4000-8000-000000000172")
	first, err := store.Write(t.Context(), id, "git-diff.patch", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if first.URI != "artifact://00000000-0000-4000-8000-000000000172/git-diff.patch" || first.SizeBytes != 5 || first.SHA256 != "a7937b64b8caa58f03721bb6bacf5c78cb235febe0e70b1b84cd99541461a08e" {
		t.Fatalf("artifact contract changed: %+v", first)
	}
	retry, err := store.Write(t.Context(), id, "git-diff.patch", []byte("first"))
	if err != nil || retry != first {
		t.Fatalf("immutable retry failed: %+v %v", retry, err)
	}
	if _, err := store.Write(t.Context(), id, "git-diff.patch", []byte("second")); !errors.Is(err, artifacts.ErrImmutable) {
		t.Fatalf("existing content was overwritten: %v", err)
	}
	reopened, err := artifacts.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	content, err := reopened.Read(t.Context(), id, first.URI)
	if err != nil || !bytes.Equal(content, []byte("first")) {
		t.Fatalf("durable content was not retained: %v", err)
	}
}

type boundedReader struct {
	data    *bytes.Reader
	largest int
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if len(p) > 1024*1024 {
		return 0, errors.New("read exceeded one MiB")
	}
	r.largest = max(r.largest, len(p))
	return r.data.Read(p)
}

func TestStreamPublicationHasBoundedReadsAndConcurrentImmutableRetries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := artifacts.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	data := bytes.Repeat([]byte("output"), 500000)
	done := make(chan error, 8)
	for range cap(done) {
		go func() {
			reader := &boundedReader{data: bytes.NewReader(data)}
			result, err := store.WriteStream(t.Context(), id, "worktree.tar", reader)
			if err == nil && (result.SizeBytes != 3000000 || reader.largest == 0) {
				err = errors.New("stream was not stored with bounded reads")
			}
			done <- err
		}()
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	uri, _ := artifacts.URI(id, "worktree.tar")
	if got, err := store.Read(t.Context(), id, uri); err != nil || !bytes.Equal(got, data) {
		t.Fatal("concurrent publication changed content")
	}
	entries, err := os.ReadDir(filepath.Join(root, id.String()))
	if err != nil || len(entries) != 1 || entries[0].Name() != "worktree.tar" {
		t.Fatal("stream publication leaked temporary files")
	}
}

type interruptedReader struct {
	cancel context.CancelFunc
	done   bool
	fail   bool
}

func (r *interruptedReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		if r.cancel != nil {
			r.cancel()
		}
		return copy(p, []byte("partial output")), nil
	}
	if r.fail {
		return 0, errors.New("injected source failure")
	}
	return 0, io.EOF
}

func TestInterruptedStreamsNeverPublishPartialContent(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "artifacts")
		store, err := artifacts.NewLocalStore(root)
		if err != nil {
			t.Fatal(err)
		}
		id := uuid.New()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		reader := &interruptedReader{fail: !cancelled}
		if cancelled {
			reader.cancel = cancel
		}
		if _, err := store.WriteStream(ctx, id, "output.txt", reader); err == nil || cancelled && !errors.Is(err, context.Canceled) {
			t.Fatalf("interruption was not returned: %v", err)
		}
		uri, _ := artifacts.URI(id, "output.txt")
		if _, err := store.Read(t.Context(), id, uri); !errors.Is(err, artifacts.ErrContent) {
			t.Fatalf("partial content was published: %v", err)
		}
		if _, err := store.Write(t.Context(), id, "output.txt", []byte("complete")); err != nil {
			t.Fatalf("interruption poisoned publication retry: %v", err)
		}
	}
}

func TestContentRejectsForeignURIsUnsafeNamesAndSymlinks(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "artifacts")
	store, err := artifacts.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id, foreign := uuid.New(), uuid.New()
	for _, name := range []string{"", ".hidden", "../secret", "nested/secret", "UPPER", "bad\x00name", strings.Repeat("x", 129)} {
		if _, err := store.Write(t.Context(), id, name, []byte("bad")); !errors.Is(err, artifacts.ErrContent) {
			t.Fatalf("unsafe name accepted: %v", err)
		}
	}
	first, err := store.Write(t.Context(), id, "output.txt", []byte("owned"))
	if err != nil {
		t.Fatal(err)
	}
	for _, uri := range []string{first.URI, "file://" + root, "artifact://" + foreign.String() + "/../output.txt", "artifact://" + foreign.String() + "/nested/output.txt"} {
		if _, err := store.Read(t.Context(), foreign, uri); !errors.Is(err, artifacts.ErrContent) {
			t.Fatalf("unowned URI accepted: %v", err)
		}
	}
	outside := filepath.Join(base, "secret")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, id.String(), "output.txt")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(t.Context(), id, first.URI); !errors.Is(err, artifacts.ErrContent) {
		t.Fatalf("file symlink was followed: %v", err)
	}
	if _, err := store.Write(t.Context(), id, "output.txt", []byte("private")); !errors.Is(err, artifacts.ErrContent) {
		t.Fatalf("file symlink was reused: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, id.String()), filepath.Join(root, foreign.String())); err != nil {
		t.Fatal(err)
	}
	uri, _ := artifacts.URI(foreign, "output.txt")
	if _, err := store.Read(t.Context(), foreign, uri); !errors.Is(err, artifacts.ErrContent) {
		t.Fatalf("Run directory symlink was followed: %v", err)
	}
	if _, err := store.Write(t.Context(), foreign, "new.txt", []byte("bad")); !errors.Is(err, artifacts.ErrContent) {
		t.Fatalf("Run directory symlink was used for publication: %v", err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, id.String(), "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	uri, _ = artifacts.URI(id, "pipe")
	if _, err := store.Read(t.Context(), id, uri); !errors.Is(err, artifacts.ErrContent) {
		t.Fatalf("special file was read: %v", err)
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "private" {
		t.Fatal("artifact operations changed an outside file")
	}
}

func TestPublishedContentHasPrivateReadablePermissions(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	store, err := artifacts.NewLocalStore(root)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := store.Write(t.Context(), id, "seed.txt", []byte("seed")); err != nil {
		t.Fatal(err)
	}
	previous := syscall.Umask(0777)
	defer syscall.Umask(previous)
	if _, err := store.Write(t.Context(), id, "private.txt", []byte("retained")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, id.String(), "private.txt"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("published bytes did not retain private readable permissions")
	}
}
