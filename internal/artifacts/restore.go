package artifacts

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// RestoreArchive creates a new owned directory; it never overlays a repository.
// No archive entry may traverse a symlink, even one pointing within the root.
func RestoreArchive(ctx context.Context, source io.Reader, destination string) (result error) {
	resolved, err := resolve(destination)
	if source == nil || !filepath.IsAbs(destination) || resolved != destination || destination == "/" {
		return ErrArchive
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return ErrArchive
	}
	defer func() {
		if result != nil {
			result = errors.Join(ErrArchive, ctx.Err(), os.RemoveAll(destination))
		}
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	input := &archiveCountReader{Reader: contextReader{ctx, source}}
	reader := tar.NewReader(input)
	seen := map[string]byte{}
	links := map[string]string{}
	type directoryMode struct {
		name string
		mode os.FileMode
	}
	var directories []directoryMode
	for {
		before := input.count
		header, err := reader.Next()
		if err == io.EOF {
			if input.count-before < 1024 {
				return ErrArchive
			}
			buffer := make([]byte, 32768)
			for {
				n, e := input.Read(buffer)
				if !bytes.Equal(buffer[:n], make([]byte, n)) {
					return ErrArchive
				}
				if e == io.EOF {
					break
				}
				if e != nil {
					return e
				}
			}
			if input.count%512 != 0 {
				return ErrArchive
			}
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		_, duplicate := seen[name]
		if !archivePath(name) || duplicate || header.Size < 0 {
			return ErrArchive
		}
		if err = restoreParents(root, path.Dir(name)); err != nil {
			return err
		}
		mode := os.FileMode(header.Mode) & 0777
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return ErrArchive
			}
			if err = root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err := root.Lstat(name)
			if err != nil || !info.IsDir() {
				return ErrArchive
			}
			directories = append(directories, directoryMode{name, mode})
		case tar.TypeReg, tar.TypeRegA:
			file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(file, contextReader{ctx, reader})
			chmodErr := file.Chmod(mode)
			closeErr := file.Close()
			if n != header.Size || copyErr != nil || chmodErr != nil || closeErr != nil {
				return ErrArchive
			}
		case tar.TypeSymlink:
			target := header.Linkname
			for _, part := range strings.Split(target, "/") {
				if strings.EqualFold(part, ".git") {
					return ErrArchive
				}
			}
			if header.Size != 0 || target == "" || path.IsAbs(target) || !archivePath(path.Clean(path.Join(path.Dir(name), target))) {
				return ErrArchive
			}
			if err = root.Symlink(target, name); err != nil {
				return err
			}
			links[name] = target
		case tar.TypeLink:
			target := header.Linkname
			if header.Size != 0 || !archivePath(target) || (seen[target] != tar.TypeReg && seen[target] != tar.TypeRegA && seen[target] != tar.TypeLink) {
				return ErrArchive
			}
			if err = root.Link(target, name); err != nil {
				return err
			}
		default:
			return ErrArchive
		}
		seen[name] = header.Typeflag
	}
	if err := validateArchiveLinks(links); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err = root.Chmod(directories[i].name, directories[i].mode); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func validateArchiveLinks(links map[string]string) error {
	for name := range links {
		var resolved []string
		pending := strings.Split(name, "/")
		traversals := 0
		for len(pending) > 0 {
			part := pending[0]
			pending = pending[1:]
			switch part {
			case "", ".":
				continue
			case "..":
				if len(resolved) == 0 {
					return ErrArchive
				}
				resolved = resolved[:len(resolved)-1]
			default:
				resolved = append(resolved, part)
				if target, ok := links[strings.Join(resolved, "/")]; ok {
					traversals++
					// Bound traversal so cycles and excessive chains fail closed.
					if traversals > 40 {
						return ErrArchive
					}
					resolved = resolved[:len(resolved)-1]
					pending = append(strings.Split(target, "/"), pending...)
				}
			}
		}
	}
	return nil
}

func archivePath(name string) bool {
	if name == "" || name == "." || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\\") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.EqualFold(part, ".git") {
			return false
		}
	}
	return true
}

func restoreParents(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(name, "/") {
		current = path.Join(current, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil || !info.IsDir() {
			return ErrArchive
		}
	}
	return nil
}

type archiveCountReader struct {
	io.Reader
	count int64
}

func (r *archiveCountReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.count += int64(n)
	return n, err
}
