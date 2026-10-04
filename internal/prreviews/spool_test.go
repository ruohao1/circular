package prreviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestReviewSpoolReplaysIdenticalAndRejectsConflictingReport(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := NewSpool(directory, Context{ReviewID: uuid.New(), RunID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"summary":"Reviewed the change","coverage":"complete","findings":[],"checks":[],"limitations":[]}`)
	first, err := spool.Submit(raw)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			second, err := spool.Submit(raw)
			if err != nil || first != second {
				t.Error("identical replay changed receipt", err)
			}
		})
	}
	wg.Wait()
	changed := bytes.ReplaceAll(raw, []byte("Reviewed the change"), []byte("Different assessment"))
	if _, err := spool.Submit(changed); !errors.Is(err, ErrConflictingReport) {
		t.Fatal("conflict accepted", err)
	}
	var output bytes.Buffer
	if err := spool.Publish(&output); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(output.Bytes(), []byte("circular.pr_review.submitted")) != 1 || bytes.Contains(output.Bytes(), []byte("Different assessment")) {
		t.Fatal("replaced/duplicated report")
	}
	if err := os.Remove(filepath.Join(directory, "report.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(directory, "report.json")); err != nil {
		t.Fatal(err)
	}
	if err := spool.Publish(&bytes.Buffer{}); err == nil {
		t.Fatal("linked report accepted")
	}
}
func TestReviewSpoolMissingReportPublishesNothing(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(d, Context{ReviewID: uuid.New(), RunID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := s.Publish(&out); err != nil || out.Len() != 0 {
		t.Fatal(err)
	}
}
