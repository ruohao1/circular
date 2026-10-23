package completion

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestCompletionSpoolImmutableAndBounded(t *testing.T) {
	contract, err := SealContract(Policy{Mode: "direct_completion", Requirements: []Requirement{{Key: "tests", Description: "Show tests"}}}, CheckConfig{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	spool, err := NewSpool(dir, contract)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = spool.Read(); !os.IsNotExist(err) {
		t.Fatal("missing report", err)
	}
	if err = spool.Submit(Report{Result: "Missing evidence"}); err == nil {
		t.Fatal("incomplete report saved")
	}
	report := Report{Result: "Implemented", Evidence: []Evidence{{Key: "tests", Detail: "Passed"}}}
	var wg sync.WaitGroup
	fail := make(chan error, 8)
	for range 8 {
		wg.Go(func() { fail <- spool.Submit(report) })
	}
	wg.Wait()
	close(fail)
	for err := range fail {
		if err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := OpenSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := restarted.Read()
	if err != nil || !reflect.DeepEqual(saved, report) {
		t.Fatal(saved, err)
	}
	changed := report
	changed.Result = "Different"
	if err = restarted.Submit(changed); err == nil {
		t.Fatal("accepted conflicting report")
	}
	var out bytes.Buffer
	if err = spool.Publish(&out); err != nil || !strings.Contains(out.String(), "circular.task.completion.reported") {
		t.Fatal(out.String(), err)
	}
	if err = os.Remove(filepath.Join(dir, "report.json")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "report")
	if err = os.WriteFile(outside, []byte(`{"result":"Forged","evidence":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(dir, "report.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = spool.Read(); err == nil {
		t.Fatal("followed untrusted report symlink")
	}
	if err = os.Remove(filepath.Join(dir, "report.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "report.json"), bytes.Repeat([]byte("x"), MaxReportBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = spool.Read(); err == nil {
		t.Fatal("unbounded file read")
	}
	forged := contract
	forged.Policy.Mode = "review_required"
	if _, err = NewSpool(dir, forged); err == nil {
		t.Fatal("forged contract accepted")
	}
}
