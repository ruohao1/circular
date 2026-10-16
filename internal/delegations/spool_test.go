package delegations

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestDelegationSpoolReplay(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(dir)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Request(fixtureRequest()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	changed := fixtureRequest()
	changed.Objective.Description = "Changed"
	if _, err := s.Request(changed); err == nil {
		t.Fatal("changed replay accepted")
	}
	changed = fixtureRequest()
	changed.RequestKey = "another"
	if _, err := s.Request(changed); err == nil {
		t.Fatal("second child request accepted")
	}
	var out bytes.Buffer
	if err := s.Publish(&out); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(out.Bytes(), []byte("\n")) != 1 || !bytes.Contains(out.Bytes(), []byte(`"type":"circular.delegation.requested"`)) {
		t.Fatal("request publication duplicated or absent")
	}
}

func TestDelegationSpoolRejectsUnsafeFiles(t *testing.T) {
	for _, name := range []string{".lock", "request.json"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			s, err := NewSpool(dir)
			if err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Request(fixtureRequest()); err == nil {
				t.Fatal("followed symlink")
			}
			got, _ := os.ReadFile(outside)
			if string(got) != "preserve" {
				t.Fatal("changed external file")
			}
		})
	}
}
