package codexworkload

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestReviewForwardingCannotSplitPartialCLIRecord(t *testing.T) {
	var output bytes.Buffer
	stream := &reviewOutput{writer: &output}
	if _, err := stream.Write([]byte(`{"type":"cli.output","text":"partial`)); err != nil {
		t.Fatal(err)
	}
	if _, err := (recordWriter{stream}).Write([]byte("{\"type\":\"circular.pr_review.submitted\"}\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Write([]byte(" complete\"}\n")); err != nil {
		t.Fatal(err)
	}
	if err := stream.finish(); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var report, cli map[string]string
	if decoder.Decode(&report) != nil || decoder.Decode(&cli) != nil || report["type"] != "circular.pr_review.submitted" || cli["text"] != "partial complete" {
		t.Fatal("interleaved worker event stream")
	}
}
