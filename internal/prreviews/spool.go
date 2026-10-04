package prreviews

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/google/uuid"
)

type Spool struct {
	directory string
	context   Context
}

func NewSpool(directory string, value Context) (*Spool, error) {
	root, err := openContextDirectory(directory)
	if err != nil {
		return nil, ErrInvalidReport
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil || info.Mode().Perm() != 0700 || value.RunID == uuid.Nil || value.ReviewID == uuid.Nil {
		return nil, ErrInvalidReport
	}
	return &Spool{directory, value}, nil
}
func (s *Spool) locked(action func(*os.Root) error) error {
	root, err := openContextDirectory(s.directory)
	if err != nil {
		return ErrInvalidReport
	}
	defer root.Close()
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return ErrInvalidReport
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrInvalidReport
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return ErrInvalidReport
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return action(root)
}
func (s *Spool) read(root *os.Root) (ValidatedReport, error) {
	raw, err := readContextFile(root, "report.json", MaxReportBytes)
	if err != nil {
		return ValidatedReport{}, ErrInvalidReport
	}
	return ValidateReport(raw, s.context)
}
func (s *Spool) Submit(raw json.RawMessage) (string, error) {
	report, err := ValidateReport(raw, s.context)
	if err != nil {
		return "", err
	}
	err = s.locked(func(root *os.Root) error {
		if _, err := root.Lstat("report.json"); err == nil {
			existing, err := s.read(root)
			if err != nil {
				return err
			}
			if existing.SHA256 != report.SHA256 {
				return ErrConflictingReport
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrInvalidReport
		}
		data, err := json.Marshal(report.Report)
		if err != nil {
			return ErrInvalidReport
		}
		temporary := ".report-" + uuid.NewString()
		file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return ErrInvalidReport
		}
		defer root.Remove(temporary)
		defer file.Close()
		if _, err := file.Write(data); err != nil {
			return ErrInvalidReport
		}
		if errors.Join(file.Sync(), file.Close()) != nil {
			return ErrInvalidReport
		}
		if root.Rename(temporary, "report.json") != nil {
			return ErrInvalidReport
		}
		dir, err := root.Open(".")
		if err != nil {
			return ErrInvalidReport
		}
		return errors.Join(dir.Sync(), dir.Close())
	})
	if err != nil {
		return "", err
	}
	return report.SHA256, nil
}
func (s *Spool) Publish(writer io.Writer) error {
	return s.publish(writer, "")
}
func (s *Spool) publish(writer io.Writer, expectedSHA string) error {
	return s.locked(func(root *os.Root) error {
		if _, err := root.Lstat("report.json"); errors.Is(err, os.ErrNotExist) {
			if expectedSHA != "" {
				return ErrInvalidReport
			}
			return nil
		} else if err != nil {
			return ErrInvalidReport
		}
		report, err := s.read(root)
		if err != nil {
			return err
		}
		if expectedSHA != "" && report.SHA256 != expectedSHA {
			return ErrConflictingReport
		}
		return json.NewEncoder(writer).Encode(map[string]any{"type": "circular.pr_review.submitted", "report": report.Report})
	})
}
