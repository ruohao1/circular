package delegations

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

type Result struct {
	ChildRunID uuid.UUID          `json:"child_run_id"`
	Status     runstate.Status    `json:"status"`
	Error      string             `json:"error"`
	Report     *artifacts.Content `json:"report"`
	Patch      *artifacts.Content `json:"patch"`
}

type Context struct {
	RunID      uuid.UUID  `json:"run_id"`
	Checkpoint Checkpoint `json:"checkpoint"`
	Result     Result     `json:"result"`
}

func (r Result) Validate() error {
	if r.ChildRunID == uuid.Nil || !r.Status.Terminal() || !utf8.ValidString(r.Error) || strings.ContainsRune(r.Error, 0) || utf8.RuneCountInString(r.Error) > 4000 {
		return ErrInvalid
	}
	for name, content := range map[string]*artifacts.Content{"delegation-report.txt": r.Report, "git-diff.patch": r.Patch} {
		if content == nil {
			continue
		}
		uri, _ := artifacts.URI(r.ChildRunID, name)
		if content.URI != uri || content.SizeBytes < 0 || !prreviews.DigestPattern.MatchString(content.SHA256) {
			return ErrInvalid
		}
	}
	return nil
}

func ContextFingerprint(value Context) (string, error) {
	if value.Checkpoint.Validate(value.RunID) != nil || value.Result.Validate() != nil || value.Result.ChildRunID == value.RunID {
		return "", ErrInvalid
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (v Context) files() map[string]artifacts.Content {
	files := map[string]artifacts.Content{"progress.json": v.Checkpoint.Progress}
	if v.Result.Report != nil {
		files["report.txt"] = *v.Result.Report
	}
	if v.Result.Patch != nil {
		files["diff.patch"] = *v.Result.Patch
	}
	return files
}

func contextRoot(directory string) (*os.Root, error) {
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || resolved != directory {
		return nil, ErrInvalid
	}
	return os.OpenRoot(directory)
}

// ReadContext verifies every file before any workload obtains credentials.
func ReadContext(directory string) (Context, error) {
	var value Context
	root, err := contextRoot(directory)
	if err != nil {
		return value, err
	}
	defer root.Close()
	f, err := openContextFile(root, "context.json")
	if err != nil {
		return value, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, 64*1024+1))
	f.Close()
	if err != nil || len(raw) > 64*1024 || prreviews.DecodeStrict(raw, &value) != nil {
		return value, ErrInvalid
	}
	if _, err = ContextFingerprint(value); err != nil {
		return value, err
	}
	for name, want := range value.files() {
		file, err := openContextFile(root, name)
		if err != nil {
			return value, err
		}
		h := sha256.New()
		n, copyErr := io.Copy(h, io.LimitReader(file, want.SizeBytes+1))
		file.Close()
		if copyErr != nil || n != want.SizeBytes || hex.EncodeToString(h.Sum(nil)) != want.SHA256 {
			return value, ErrInvalid
		}
	}
	return value, nil
}

func openContextFile(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}

// WriteContext atomically publishes only this Run's verified, read-only inputs.
func WriteContext(directory string, value Context, files map[string]io.Reader) error {
	want, err := ContextFingerprint(value)
	if err != nil || filepath.Base(directory) != value.RunID.String() || !filepath.IsAbs(directory) {
		return ErrInvalid
	}
	if len(files) != len(value.files()) {
		return ErrInvalid
	}
	parent := filepath.Dir(directory)
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	root, err := contextRoot(parent)
	if err != nil {
		return err
	}
	defer root.Close()
	verify := func() error {
		saved, err := ReadContext(directory)
		if err != nil {
			return err
		}
		got, _ := ContextFingerprint(saved)
		if got != want {
			return ErrInvalid
		}
		return nil
	}
	if _, err = root.Lstat(value.RunID.String()); err == nil {
		return verify()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	staging := "." + value.RunID.String() + "." + uuid.NewString()
	if err = root.Mkdir(staging, 0700); err != nil {
		return err
	}
	defer root.RemoveAll(staging)
	temp, err := root.OpenRoot(staging)
	if err != nil {
		return err
	}
	defer temp.Close()
	raw, _ := json.Marshal(value)
	writes := map[string]io.Reader{"context.json": bytes.NewReader(raw)}
	for name, content := range value.files() {
		if files[name] == nil || content.SizeBytes == int64(^uint64(0)>>1) {
			return ErrInvalid
		}
		writes[name] = io.LimitReader(files[name], content.SizeBytes+1)
	}
	for name, reader := range writes {
		file, err := temp.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0444)
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(file, reader)
		if err = errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
			return err
		}
	}
	if _, err = ReadContext(filepath.Join(parent, staging)); err != nil {
		return err
	}
	if err = temp.Chmod(".", 0755); err != nil {
		return err
	}
	if err = syncContextDirectory(temp); err != nil {
		return err
	}
	if err = root.Rename(staging, value.RunID.String()); err != nil {
		return verify()
	}
	if err = syncContextDirectory(root); err != nil {
		return err
	}
	return verify()
}

func syncContextDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
