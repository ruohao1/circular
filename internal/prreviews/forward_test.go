package prreviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type failingReportWriter struct{}

func (failingReportWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestReportForwarderAcknowledgesOnlyVerifiedForwardedReports(t *testing.T) {
	for _, scenario := range []string{"missing", "wrong_checksum", "output_failure", "success"} {
		t.Run(scenario, func(t *testing.T) {
			directory, err := os.MkdirTemp("/tmp", "review-forward-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(directory)
			spool, err := NewSpool(directory, Context{ReviewID: uuid.New(), RunID: uuid.New()})
			if err != nil {
				t.Fatal(err)
			}
			checksum := strings.Repeat("a", 64)
			if scenario != "missing" {
				checksum, err = spool.Submit(json.RawMessage(`{"summary":"Retain these findings","coverage":"complete","findings":[],"checks":[],"limitations":[]}`))
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "wrong_checksum" {
				checksum = strings.Repeat("a", 64)
			}
			var output bytes.Buffer
			var writer io.Writer = &output
			if scenario == "output_failure" {
				writer = failingReportWriter{}
			}
			forwarder, err := StartReportForwarder(directory, spool, writer)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				err = ForwardReport(t.Context(), directory, checksum)
				if (err == nil) != (scenario == "success") {
					t.Fatalf("wrong acknowledgement: %s %v", scenario, err)
				}
			}
			err = forwarder.Finish()
			if scenario == "output_failure" && !errors.Is(err, io.ErrClosedPipe) {
				t.Fatal(err)
			}
			if scenario == "success" && bytes.Count(output.Bytes(), []byte("circular.pr_review.submitted")) != 1 {
				t.Fatal("replay emitted duplicate report")
			}
			if scenario == "missing" && output.Len() != 0 {
				t.Fatal("acknowledged missing report")
			}
		})
	}
}
