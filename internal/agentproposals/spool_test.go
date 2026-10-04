package agentproposals

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSpoolDeduplicatesConcurrentRequestsAndBoundsRunProposals(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewSpool(directory)
	if err != nil {
		t.Fatal(err)
	}
	input := Input{Name: "Engineer", Purpose: "Build features", Instructions: "Read the project conventions"}
	var wait sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wait.Go(func() {
			draft, err := s.Propose(input)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- draft.ID
		})
	}
	wait.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first != "" && first != id {
			t.Fatal("duplicate draft")
		}
		first = id
	}
	for i := 1; i < MaxProposals; i++ {
		input.Name = fmt.Sprintf("Engineer %d", i)
		if _, err := s.Propose(input); err != nil {
			t.Fatal(err)
		}
	}
	input.Name = "One too many"
	if _, err := s.Propose(input); !errors.Is(err, ErrLimit) {
		t.Fatal("unbounded proposals", err)
	}
	input.Name = "Engineer"
	if draft, err := s.Propose(input); err != nil || draft.ID != first {
		t.Fatal("retry at limit failed", err)
	}
}

func TestUntrustedDraftFilesFailWithoutPublishingTheirContent(t *testing.T) {
	for _, kind := range []string{"symlink", "oversized", "unknown fields", "missing fields"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.Chmod(directory, 0700); err != nil {
				t.Fatal(err)
			}
			s, err := NewSpool(directory)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "untrusted.json")
			switch kind {
			case "symlink":
				secret := filepath.Join(t.TempDir(), "credential")
				if err := os.WriteFile(secret, []byte("do-not-publish"), 0600); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(secret, path)
			case "oversized":
				err = os.WriteFile(path, []byte(strings.Repeat("x", MaxDraftBytes+1)), 0600)
			case "unknown fields":
				err = os.WriteFile(path, []byte(`{"id":"345927de-00b5-4a8f-ad8d-2a2dcd21f144","name":"Role","purpose":"Test","instructions":"Read","project_id":"untrusted"}`), 0600)
			case "missing fields":
				err = os.WriteFile(path, []byte(`{"id":"345927de-00b5-4a8f-ad8d-2a2dcd21f144"}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := s.Publish(&output); err == nil || output.Len() != 0 {
				t.Fatal("untrusted file published", err)
			}
		})
	}
}
