package git

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

var errOwnership = errors.New("worktree ownership receipt could not be verified")

type ownership struct {
	marker os.FileInfo
	target [2]uint64
}

func markerName(target string, runID uuid.UUID) (string, error) {
	name := filepath.Base(target)
	if strings.HasPrefix(name, "."+runID.String()+".worktree-") {
		return name + ".owner", nil
	}
	if name != runID.String() {
		return "", errOwnership
	}
	return "." + runID.String() + ".owner", nil
}

func markerPrefix(runID, repositoryID uuid.UUID) []byte {
	prefix := append([]byte("circular-worktree-owner\x00\x01"), runID[:]...)
	return append(prefix, repositoryID[:]...)
}

func targetIdentity(root *os.Root, name string) (*[2]uint64, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errOwnership
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, errOwnership
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errOwnership
	}
	return &[2]uint64{uint64(stat.Dev), stat.Ino}, nil
}

func receiptRoot(target string) (*os.Root, error) {
	parent := filepath.Dir(target)
	if !canonical(parent) {
		return nil, errOwnership
	}
	file, err := os.OpenFile(parent, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, err
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, errOwnership
	}
	return root, nil
}

func readMarker(root *os.Root, name string, prefix []byte) (*ownership, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errOwnership
	}
	defer file.Close()
	info, err := file.Stat()
	size := len(prefix) + 32
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(size) {
		return nil, errOwnership
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(size+1)))
	if err != nil || len(data) != size || !bytes.HasPrefix(data, prefix) {
		return nil, errOwnership
	}
	identity := data[len(prefix):]
	// Python encodes each value as an unsigned 128-bit integer. Unix device
	// and inode values fit in uint64; larger receipts cannot identify this host.
	if !bytes.Equal(identity[:8], make([]byte, 8)) || !bytes.Equal(identity[16:24], make([]byte, 8)) {
		return nil, errOwnership
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return nil, errOwnership
	}
	return &ownership{marker: info, target: [2]uint64{binary.BigEndian.Uint64(identity[8:16]), binary.BigEndian.Uint64(identity[24:32])}}, nil
}

func hasMarker(target string, runID, repositoryID uuid.UUID) (bool, error) {
	name, err := markerName(target, runID)
	if err != nil {
		return false, err
	}
	root, err := receiptRoot(target)
	if err != nil {
		return false, err
	}
	defer root.Close()
	marker, err := readMarker(root, name, markerPrefix(runID, repositoryID))
	if err != nil || marker == nil {
		return false, err
	}
	identity, err := targetIdentity(root, filepath.Base(target))
	if err != nil || identity != nil && *identity != marker.target {
		return false, errOwnership
	}
	return true, nil
}

func syncRoot(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func createMarker(target string, runID, repositoryID uuid.UUID) (result error) {
	name, err := markerName(target, runID)
	if err != nil {
		return err
	}
	root, err := receiptRoot(target)
	if err != nil {
		return err
	}
	defer root.Close()
	identity, err := targetIdentity(root, filepath.Base(target))
	if err != nil || identity == nil {
		return errOwnership
	}
	payload := append(markerPrefix(runID, repositoryID), make([]byte, 32)...)
	binary.BigEndian.PutUint64(payload[len(payload)-24:len(payload)-16], identity[0])
	binary.BigEndian.PutUint64(payload[len(payload)-8:], identity[1])
	temporary := "." + runID.String() + ".owner-" + uuid.NewString() + ".tmp"
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
		if err := root.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
		result = errors.Join(result, syncRoot(root))
	}()
	if _, err := file.Write(payload); err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := root.Link(temporary, name); err != nil {
		return err
	}
	return syncRoot(root)
}

func removeMarker(target string, runID, repositoryID uuid.UUID) error {
	name, err := markerName(target, runID)
	if err != nil {
		return err
	}
	root, err := receiptRoot(target)
	if err != nil {
		return err
	}
	defer root.Close()
	identity, err := targetIdentity(root, filepath.Base(target))
	if err != nil || identity != nil {
		return errOwnership
	}
	marker, err := readMarker(root, name, markerPrefix(runID, repositoryID))
	if err != nil || marker == nil {
		return err
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(current, marker.marker) {
		return errOwnership
	}
	identity, err = targetIdentity(root, filepath.Base(target))
	if err != nil || identity != nil {
		return errOwnership
	}
	if err := syncRoot(root); err != nil {
		return err
	}
	if err := root.Remove(name); err != nil {
		return err
	}
	return syncRoot(root)
}
