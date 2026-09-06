package artifacts

import (
	"context"
	"errors"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrArchive = errors.New("worktree output could not be safely archived")

// WriteArchive publishes only a complete archive. The temporary file is outside
// the worktree; file contents are streamed, while traversal metadata grows with
// entry count rather than file size.
func (s *LocalStore) WriteArchive(ctx context.Context, runID uuid.UUID, worktree string) (stored Content, result error) {
	if err := ctx.Err(); err != nil {
		return Content{}, err
	}
	file, err := os.CreateTemp("", "circular-worktree-*.tar")
	if err != nil {
		return Content{}, ErrArchive
	}
	// Keep the disk-backed spool anonymous, as Python's TemporaryFile does.
	// A killed worker must not leave an unreferenced archive in the temp root.
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		return Content{}, ErrArchive
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = errors.Join(result, ErrArchive)
		}
	}()
	if err := Archive(ctx, worktree, file); err != nil {
		return Content{}, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Content{}, ErrArchive
	}
	return s.WriteStream(ctx, runID, "worktree.tar", file)
}

// Archive streams the existing Python worktree.tar format, excluding .git at
// every depth. It never follows symlinks. The caller must stop execution first;
// a failure leaves the destination incomplete and it must not be published.
func Archive(ctx context.Context, path string, destination io.Writer) (result error) {
	defer func() {
		if result != nil {
			result = errors.Join(ErrArchive, ctx.Err())
		}
	}()
	resolved, err := resolve(path)
	if err != nil || !filepath.IsAbs(path) || resolved != path || path == string(filepath.Separator) || destination == nil {
		return ErrArchive
	}
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() {
		return ErrArchive
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return ErrArchive
	}
	archive := archiveStream{ctx: ctx, destination: destination, users: map[uint32]string{}, groups: map[uint32]string{}, inodes: map[[2]uint64]string{}}
	if err := archive.directory(root, ""); err != nil {
		return err
	}
	if err := archive.zeros(1024); err != nil {
		return err
	}
	return archive.zeros((10240 - archive.offset%10240) % 10240)
}

type archiveStream struct {
	ctx           context.Context
	destination   io.Writer
	offset        int64
	users, groups map[uint32]string
	inodes        map[[2]uint64]string
	buffer        []byte
}

func (a *archiveStream) Write(data []byte) (int, error) {
	if err := a.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := a.destination.Write(data)
	a.offset += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (a *archiveStream) zeros(count int64) error {
	_, err := a.Write(make([]byte, count))
	return err
}

func (a *archiveStream) directory(root *os.Root, prefix string) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	names, err := file.Readdirnames(-1)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	sort.Slice(names, func(i, j int) bool { return pythonNameLess(names[i], names[j]) })
	var directories []string
	directoryInfo := map[string]os.FileInfo{}
	for _, name := range names {
		if name == ".git" {
			continue
		}
		if err := a.ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return ErrArchive
		}
		if err := a.entry(root, name, prefix+name, info); err != nil {
			return err
		}
		if info.IsDir() {
			directories = append(directories, name)
			directoryInfo[name] = info
		}
	}
	// os.walk emits all siblings before descending into sorted directories.
	for _, name := range directories {
		file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		before, err := file.Stat()
		file.Close()
		if err != nil || !os.SameFile(before, directoryInfo[name]) {
			return ErrArchive
		}
		child, err := root.OpenRoot(name)
		if err != nil {
			return err
		}
		after, err := child.Stat(".")
		if err == nil && !os.SameFile(before, after) {
			err = ErrArchive
		}
		if err == nil {
			err = a.directory(child, prefix+name+"/")
		}
		closeErr := child.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

func (a *archiveStream) entry(root *os.Root, name, archiveName string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrArchive
	}
	uname, ok := a.users[stat.Uid]
	if !ok {
		if u, err := user.LookupId(strconv.FormatUint(uint64(stat.Uid), 10)); err == nil {
			uname = u.Username
		}
		a.users[stat.Uid] = uname
	}
	gname, ok := a.groups[stat.Gid]
	if !ok {
		if g, err := user.LookupGroupId(strconv.FormatUint(uint64(stat.Gid), 10)); err == nil {
			gname = g.Name
		}
		a.groups[stat.Gid] = gname
	}
	header := archiveHeader{name: archiveName, mode: int64(stat.Mode & 07777), uid: int64(stat.Uid), gid: int64(stat.Gid), size: info.Size(), mtime: float64(info.ModTime().Unix()) + float64(info.ModTime().Nanosecond())/1e9, kind: '0', uname: uname, gname: gname}
	if info.IsDir() {
		header.name += "/"
		header.kind, header.size = '5', 0
	} else if info.Mode()&os.ModeSymlink != 0 {
		link, err := root.Readlink(name)
		if err != nil {
			return err
		}
		header.kind, header.size, header.link = '2', 0, link
	} else {
		key := [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}
		if prior, ok := a.inodes[key]; ok && stat.Nlink > 1 && prior != archiveName {
			header.kind, header.size, header.link = '1', 0, prior
		} else if stat.Ino != 0 {
			a.inodes[key] = archiveName
		}
	}
	if err := a.header(header); err != nil {
		return err
	}
	if header.kind != '0' {
		return nil
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(info, before) || before.Size() != info.Size() || !before.ModTime().Equal(info.ModTime()) {
		return ErrArchive
	}
	if a.buffer == nil {
		a.buffer = make([]byte, 1024*1024)
	}
	written, err := io.CopyBuffer(a, contextReader{a.ctx, io.LimitReader(file, info.Size())}, a.buffer)
	if err != nil || written != info.Size() {
		return ErrArchive
	}
	after, err := file.Stat()
	if err != nil || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return ErrArchive
	}
	return a.zeros((512 - written%512) % 512)
}

// Python sorts filenames after decoding with surrogateescape: undecodable bytes
// occupy U+DC80..U+DCFF, rather than sorting as raw UTF-8 or replacement runes.
func pythonNameLess(a, b string) bool {
	next := func(s string) (rune, int) {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			r = 0xdc00 + rune(s[0])
		}
		return r, size
	}
	for a != "" && b != "" {
		x, nx := next(a)
		y, ny := next(b)
		if x != y {
			return x < y
		}
		a, b = a[nx:], b[ny:]
	}
	return len(a) < len(b)
}
