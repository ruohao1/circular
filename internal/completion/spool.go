package completion

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
)

const maxContractBytes = 1 << 20

var ErrConflictingReport = errors.New("a different completion report was already submitted for this invocation")

type Spool struct {
	directory string
	contract  Contract
}

func ValidateContract(contract Contract) error {
	sealed, err := SealContract(contract.Policy, contract.CheckConfig)
	if err != nil {
		return err
	}
	if sealed.Fingerprint != contract.Fingerprint {
		return ErrPolicy
	}
	return nil
}
func openSpoolDirectory(directory string) (*os.Root, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, ErrReport
	}
	return os.OpenRoot(directory)
}
func readSpoolFile(root *os.Root, name string, limit int) ([]byte, error) {
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, ErrReport
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, ErrReport
	}
	return raw, nil
}
func NewSpool(directory string, contract Contract) (*Spool, error) {
	if err := ValidateContract(contract); err != nil {
		return nil, err
	}
	root, err := openSpoolDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := json.Marshal(contract)
	if err != nil || len(raw) > maxContractBytes {
		return nil, ErrPolicy
	}
	file, err := root.OpenFile("contract.json", os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if errors.Is(err, os.ErrExist) {
		saved, e := readSpoolFile(root, "contract.json", maxContractBytes)
		if e != nil || !bytes.Equal(saved, raw) {
			return nil, ErrPolicy
		}
	} else if err != nil {
		return nil, err
	} else {
		_, writeErr := file.Write(raw)
		if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
			return nil, err
		}
	}
	return &Spool{directory: directory, contract: contract}, nil
}
func OpenSpool(directory string) (*Spool, error) {
	root, err := openSpoolDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	raw, err := readSpoolFile(root, "contract.json", maxContractBytes)
	if err != nil {
		return nil, err
	}
	var contract Contract
	if prreviews.DecodeStrict(raw, &contract) != nil {
		return nil, ErrPolicy
	}
	return NewSpool(directory, contract)
}
func (s *Spool) Read() (Report, error) {
	root, err := openSpoolDirectory(s.directory)
	if err != nil {
		return Report{}, err
	}
	defer root.Close()
	raw, err := readSpoolFile(root, "report.json", MaxReportBytes)
	if err != nil {
		return Report{}, err
	}
	var report Report
	if prreviews.DecodeStrict(raw, &report) != nil {
		return report, ErrReport
	}
	return report, ValidateReport(s.contract.Policy, report)
}
func (s *Spool) Submit(report Report) error {
	if err := ValidateReport(s.contract.Policy, report); err != nil {
		return err
	}
	root, err := openSpoolDirectory(s.directory)
	if err != nil {
		return err
	}
	defer root.Close()
	lock, err := root.OpenFile(".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrReport
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	old, err := s.Read()
	if err == nil {
		saved, _ := json.Marshal(old)
		if !bytes.Equal(saved, raw) {
			return ErrConflictingReport
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	name := ".report-" + uuid.NewString()
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, writeErr := file.Write(raw)
	if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err = root.Rename(name, "report.json"); err != nil {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
func (s *Spool) Publish(writer io.Writer) error { return s.PublishVerified(writer, "") }
func (s *Spool) PublishVerified(writer io.Writer, expected string) error {
	report, err := s.Read()
	if os.IsNotExist(err) && expected == "" {
		return nil
	}
	if err != nil {
		return err
	}
	digest, err := prreviews.Fingerprint(report)
	if err != nil {
		return err
	}
	if expected != "" && digest != expected {
		return ErrConflictingReport
	}
	return json.NewEncoder(writer).Encode(map[string]any{"type": "circular.task.completion.reported", "report": report})
}
