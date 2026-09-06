package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// openDirectory pins a child directory and checks that OpenRoot did not follow
// a substituted symlink. All further traversal stays relative to this handle.
func openDirectory(parent *os.Root, name string) (*os.Root, error) {
	file, err := parent.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, errors.New("managed directory identity changed")
	}
	return root, nil
}

func removeOwned(ctx context.Context, path string) error {
	parent, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return removeEntry(ctx, parent, filepath.Base(path))
}

func removeEntry(ctx context.Context, parent *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		root, err := openDirectory(parent, name)
		if err != nil {
			return err
		}
		defer root.Close()
		opened, err := root.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return errors.New("owned directory identity changed before removal")
		}
		dir, err := root.Open(".")
		if err != nil {
			return err
		}
		names, err := dir.Readdirnames(-1)
		closeErr := dir.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		for _, child := range names {
			if err := removeEntry(ctx, root, child); err != nil {
				return err
			}
		}
		current, err := parent.Lstat(name)
		if err != nil || !os.SameFile(info, current) {
			return errors.New("owned directory identity changed during removal")
		}
	}
	err = parent.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
