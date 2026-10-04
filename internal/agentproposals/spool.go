package agentproposals

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
)

// Spool stores bounded proposals in the Run's private temporary directory.
// It has no API address, project selector, provider credentials, or DB access.
type Spool struct{ directory string }

func NewSpool(directory string) (*Spool, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, errors.New("invalid agent proposal directory")
	}
	return &Spool{directory: directory}, nil
}

func (s *Spool) Propose(input Input) (Draft, error) {
	input, err := Normalize(input)
	if err != nil {
		return Draft{}, err
	}
	lock, err := os.OpenFile(filepath.Join(s.directory, ".lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return Draft{}, errors.New("cannot save agent proposal")
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return Draft{}, errors.New("cannot save agent proposal")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	path := filepath.Join(s.directory, input.Fingerprint()+".json")
	if _, err := os.Lstat(path); err == nil {
		return readDraft(path)
	} else if !os.IsNotExist(err) {
		return Draft{}, err
	}
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return Draft{}, err
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	if count >= MaxProposals {
		return Draft{}, ErrLimit
	}
	draft := Draft{ID: uuid.NewString(), Input: input}
	encoded, _ := json.Marshal(draft)
	file, err := os.CreateTemp(s.directory, ".draft-")
	if err != nil {
		return Draft{}, err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(encoded)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return Draft{}, errors.New("cannot save agent proposal")
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func readDraft(path string) (Draft, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Draft{}, ErrInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxDraftBytes {
		return Draft{}, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxDraftBytes+1))
	if err != nil {
		return Draft{}, ErrInvalid
	}
	return Decode(data)
}

// Publish runs after the CLI exits. Its output still passes through the normal
// credential redactor and worker validation before becoming durable proposals.
func (s *Spool) Publish(writer io.Writer) error {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		return err
	}
	drafts := []Draft{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(drafts) >= MaxProposals {
			return ErrLimit
		}
		draft, err := readDraft(filepath.Join(s.directory, entry.Name()))
		if err != nil {
			return err
		}
		drafts = append(drafts, draft)
	}
	for _, draft := range drafts {
		if err := json.NewEncoder(writer).Encode(map[string]any{"type": "circular.agent.proposed", "proposal": draft}); err != nil {
			return err
		}
	}
	return nil
}
