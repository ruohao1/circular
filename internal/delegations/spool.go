package delegations

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type Spool struct{ directory string }

func NewSpool(directory string) (*Spool, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrInvalid
	}
	return &Spool{directory: directory}, nil
}

func (s *Spool) Request(request Request) (Request, error) {
	request, err := Normalize(request)
	if err != nil {
		return Request{}, err
	}
	lock, err := os.OpenFile(filepath.Join(s.directory, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return Request{}, ErrInvalid
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return Request{}, ErrInvalid
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Request{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(s.directory, "request.json")
	old, err := readPrivate(path)
	if err == nil {
		replay, err := Decode(old)
		if err != nil || request.RequestKey != replay.RequestKey || request.Fingerprint() != replay.Fingerprint() {
			return Request{}, errors.New("only one delegation request is allowed per invocation")
		}
		return replay, nil
	}
	if !os.IsNotExist(err) {
		return Request{}, ErrInvalid
	}
	raw, _ := json.Marshal(request)
	file, err := os.CreateTemp(s.directory, ".request-")
	if err != nil {
		return Request{}, err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return Request{}, errors.New("cannot save delegation request")
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return Request{}, err
	}
	return request, nil
}

func readPrivate(path string) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxRequestBytes {
		return nil, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxRequestBytes+1))
	if err != nil || len(data) > MaxRequestBytes {
		return nil, ErrInvalid
	}
	return data, nil
}

func (s *Spool) Publish(writer io.Writer) error {
	raw, err := readPrivate(filepath.Join(s.directory, "request.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	request, err := Decode(raw)
	if err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(map[string]any{"type": "circular.delegation.requested", "request": request})
}

func (s *Spool) SaveTargets(targets []Target) error {
	if err := ValidateTargets(targets); err != nil {
		return err
	}
	raw, _ := json.Marshal(targets)
	if len(raw) > MaxRequestBytes {
		return ErrInvalid
	}
	return os.WriteFile(filepath.Join(s.directory, "targets.json"), raw, 0600)
}

func (s *Spool) Targets() ([]Target, error) {
	raw, err := readPrivate(filepath.Join(s.directory, "targets.json"))
	if err != nil {
		return nil, err
	}
	var targets []Target
	if err = json.Unmarshal(raw, &targets); err != nil {
		return nil, ErrInvalid
	}
	if err = ValidateTargets(targets); err != nil {
		return nil, err
	}
	return targets, nil
}
