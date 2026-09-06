package artifacts_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
)

type archiveDestination struct {
	bytes.Buffer
	once func()
}

func (w *archiveDestination) Write(data []byte) (int, error) {
	if len(data) > 1024*1024 {
		return 0, errors.New("archive exceeded bounded writes")
	}
	if w.once != nil {
		callback := w.once
		w.once = nil
		callback()
	}
	return w.Buffer.Write(data)
}

func TestArchiveRejectsSymlinkReplacementsWithoutReadingOutsideBytes(t *testing.T) {
	for _, directory := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "worktree")
		source := filepath.Join(root, "output")
		if directory {
			archiveFile(t, filepath.Join(source, "file"), "owned")
		} else {
			archiveFile(t, source, "owned")
		}
		outside := filepath.Join(t.TempDir(), "secret")
		if directory {
			archiveFile(t, filepath.Join(outside, "file"), "must not read outside bytes")
		} else {
			archiveFile(t, outside, "must not read outside bytes")
		}
		writer := &archiveDestination{once: func() {
			if err := os.Rename(source, source+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, source); err != nil {
				t.Fatal(err)
			}
		}}
		if err := artifacts.Archive(t.Context(), root, writer); !errors.Is(err, artifacts.ErrArchive) {
			t.Fatalf("replacement path was archived: %v", err)
		}
		if bytes.Contains(writer.Bytes(), []byte("must not read outside bytes")) {
			t.Fatal("archive followed a replaced path")
		}
	}
}

func TestIncompleteArchiveNeverPublishesContentAndCancellationIsObserved(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "worktree")
	archiveFile(t, filepath.Join(root, "output"), "complete output")
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := artifacts.NewLocalStore(filepath.Join(base, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err := store.WriteArchive(t.Context(), id, root); !errors.Is(err, artifacts.ErrArchive) {
		t.Fatalf("unsupported special file was accepted: %v", err)
	}
	uri, _ := artifacts.URI(id, "worktree.tar")
	if _, err := store.Read(t.Context(), id, uri); !errors.Is(err, artifacts.ErrContent) {
		t.Fatal("partial archive became public")
	}
	if err := os.Remove(filepath.Join(root, "pipe")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := &archiveDestination{once: cancel}
	if err := artifacts.Archive(ctx, root, writer); !errors.Is(err, context.Canceled) {
		t.Fatalf("archive ignored cancellation: %v", err)
	}
	if _, err := store.WriteArchive(t.Context(), id, root); err != nil {
		t.Fatalf("failed archive poisoned retry: %v", err)
	}
}

type boundedArchiveSink struct{ size int64 }

func (w *boundedArchiveSink) Write(data []byte) (int, error) {
	if len(data) > 1024*1024 {
		return 0, errors.New("unbounded archive write")
	}
	w.size += int64(len(data))
	return len(data), nil
}

func TestArchiveStreamsCheckoutsLargerThanThirtyTwoMiB(t *testing.T) {
	root := t.TempDir()
	file, err := os.Create(filepath.Join(root, "large-output"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(33 * 1024 * 1024); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	writer := &boundedArchiveSink{}
	if err := artifacts.Archive(t.Context(), root, writer); err != nil {
		t.Fatal(err)
	}
	if writer.size != 34611200 {
		t.Fatalf("large tar framing changed: %d", writer.size)
	}
}

func TestArchivePublicationCanRetryAndSurviveWorktreeRemoval(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "worktree")
	archiveFile(t, filepath.Join(root, "output"), "retained")
	store, err := artifacts.NewLocalStore(filepath.Join(base, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	first, err := store.WriteArchive(t.Context(), id, root)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.WriteArchive(t.Context(), id, root)
	if err != nil || retry != first {
		t.Fatalf("archive retry changed immutable content: %+v %v", retry, err)
	}
	archiveFile(t, filepath.Join(root, "output"), "changed output")
	if _, err := store.WriteArchive(t.Context(), id, root); !errors.Is(err, artifacts.ErrImmutable) {
		t.Fatalf("archive bytes were replaced: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	retained, err := store.Read(t.Context(), id, retry.URI)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(retained))
	if header, err := reader.Next(); err != nil || header.Name != "output" {
		t.Fatal("retained archive is unreadable")
	}
	if content, err := io.ReadAll(reader); err != nil || string(content) != "retained" {
		t.Fatal("original output was not retained")
	}
}

func archiveFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRetainsIgnoredOutputInStableOrder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktree")
	archiveFile(t, filepath.Join(root, ".git"), "private Git metadata")
	archiveFile(t, filepath.Join(root, ".gitignore"), "ignored/\n")
	archiveFile(t, filepath.Join(root, "ignored", "output.txt"), "retained ignored output")
	archiveFile(t, filepath.Join(root, "output.txt"), "retained output")
	var archive bytes.Buffer
	if err := artifacts.Archive(t.Context(), root, &archive); err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(archive.Bytes()))
	var names []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
		if header.Name == "ignored/output.txt" {
			data, err := io.ReadAll(reader)
			if err != nil || string(data) != "retained ignored output" {
				t.Fatal("ignored output was lost")
			}
		}
	}
	if !reflect.DeepEqual(names, []string{".gitignore", "ignored/", "output.txt", "ignored/output.txt"}) {
		t.Fatalf("archive membership/order changed: %v", names)
	}
}

func TestArchiveRetainsLinksExtendedNamesAndTimestamps(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "worktree")
	archiveFile(t, filepath.Join(root, "first.txt"), "shared inode")
	archiveFile(t, filepath.Join(root, "nested", ".git", "config"), "must not archive metadata")
	archiveFile(t, filepath.Join(root, "émoji-😀-output"), "unicode")
	archiveFile(t, filepath.Join(root, strings.Repeat("long", 40)), "long name")
	archiveFile(t, filepath.Join(root, "raw-\xff"), "undecodable filename")
	archiveFile(t, filepath.Join(root, "raw-\uE000"), "Unicode sort neighbor")
	for i, name := range []string{"first.txt", "émoji-😀-output", "raw-\xff"} {
		stamp := []time.Time{time.Unix(-2, 750000000), time.Unix(0, 1), time.Unix(9000000000, 0)}[i]
		if err := os.Chtimes(filepath.Join(root, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Link(filepath.Join(root, "first.txt"), filepath.Join(root, "nested", "second.txt")); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(base, "secret")
	archiveFile(t, filepath.Join(secret, "private"), "must not read outside data")
	if err := os.Symlink(secret, filepath.Join(root, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../absent-"+strings.Repeat("é", 80), filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(filepath.Join(root, "first.txt"), 16777217, 16777218); err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	if err := artifacts.Archive(t.Context(), root, &archive); err != nil {
		t.Fatal(err)
	}
	// The historical PAX dialect includes binary names and scientific timestamps,
	// which archive/tar.Reader rejects. Preserve these published wire values.
	for _, field := range []string{"hdrcharset=BINARY\n", "mtime=-1.25\n", "mtime=1e-09\n", "path=raw-\xff\n", "path=émoji-😀-output\n", "linkpath=../absent-" + strings.Repeat("é", 80) + "\n"} {
		if !bytes.Contains(archive.Bytes(), []byte(field)) {
			t.Fatalf("legacy PAX field missing: %q", field)
		}
	}
	foundLink := false
	for offset := 0; offset+512 <= archive.Len(); {
		block := archive.Bytes()[offset : offset+512]
		size, err := strconv.ParseInt(strings.Trim(string(block[124:136]), "\x00 "), 8, 64)
		if err != nil {
			break
		}
		if block[156] == tar.TypeLink && strings.TrimRight(string(block[:100]), "\x00") == "nested/second.txt" {
			foundLink = strings.TrimRight(string(block[157:257]), "\x00") == "first.txt"
		}
		offset += 512 + int((size+511)/512)*512
	}
	if !foundLink {
		t.Fatal("shared inode was not retained as a hard link")
	}

	if archive.Len()%10240 != 0 {
		t.Fatal("archive lost the stable 20-block record framing")
	}

	if bytes.Contains(archive.Bytes(), []byte("must not read outside data")) || bytes.Contains(archive.Bytes(), []byte("must not archive metadata")) {
		t.Fatal("archive crossed its owned output tree")
	}
}
