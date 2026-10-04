package git

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/ruohao1/circular/internal/prreviews"
)

func TestReviewManifestBoundsAndTruncation(t *testing.T) {
	for _, raw := range [][]byte{[]byte("M\x00unterminated"), []byte("M\x00../outside\x00"), []byte("R100\x00one\x00"), []byte("M\x00\xff\x00"), []byte("C100\x00old\x00new\x00")} {
		if _, err := reviewFiles(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	var names bytes.Buffer
	for i := range prreviews.MaxFiles {
		fmt.Fprintf(&names, "A\x00file-%d\x00", i)
	}
	if f, err := reviewFiles(names.Bytes()); err != nil || len(f) != prreviews.MaxFiles {
		t.Fatal(err)
	}
	names.WriteString("A\x00one-over\x00")
	if _, err := reviewFiles(names.Bytes()); err == nil {
		t.Fatal("file limit ignored")
	}
	if _, _, err := reviewRanges([]byte("@@ broken")); err == nil {
		t.Fatal("truncated hunk accepted")
	}
	for _, limit := range []int{prreviews.MaxDiffBytes, prreviews.MaxContextBytes} {
		out := boundedGitOutput{limit: limit}
		if n, err := out.Write(make([]byte, limit)); err != nil || n != limit {
			t.Fatal("exact bound", err)
		}
		if _, err := out.Write([]byte{1}); err == nil || !out.exceeded {
			t.Fatal("one over bound")
		}
	}
}
