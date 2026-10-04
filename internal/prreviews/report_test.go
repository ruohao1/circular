package prreviews

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ruohao1/circular/internal/runstate"
)

func TestReportRequiresChangedLocationAndCompleteCoverage(t *testing.T) {
	ctx := Context{Files: []ChangedFile{{
		OldPath: "calc.go", NewPath: "calc.go", Status: "modified",
		BaseLines: 8, HeadLines: 8,
		BaseChanged: []LineRange{{Start: 4, End: 4}},
		HeadChanged: []LineRange{{Start: 4, End: 4}},
	}}}
	raw := []byte(`{"summary":"Wrong boundary","coverage":"complete","findings":[{"severity":"high","title":"Zero is rejected","path":"calc.go","side":"head","start_line":4,"end_line":4,"evidence":"The comparison rejects zero.","consequence":"A valid input fails.","suggested_fix":"Allow the zero boundary."}],"checks":[],"limitations":[]}`)
	report, err := ValidateReport(raw, ctx)
	if err != nil || Assess(runstate.Succeeded, &report) != AssessmentFindings {
		t.Fatalf("valid changed-line finding rejected: %v", err)
	}
	for _, replacement := range []string{"../calc.go", "/calc.go", "unknown.go"} {
		changed := bytes.ReplaceAll(raw, []byte(`"path":"calc.go"`),
			[]byte(`"path":"`+replacement+`"`))
		if _, err := ValidateReport(changed, ctx); !errors.Is(err, ErrInvalidReport) {
			t.Fatalf("accepted untrusted path %q: %v", replacement, err)
		}
	}
	changed := bytes.ReplaceAll(raw, []byte(`"start_line":4,"end_line":4`),
		[]byte(`"start_line":7,"end_line":7`))
	if _, err := ValidateReport(changed, ctx); !errors.Is(err, ErrInvalidReport) {
		t.Fatalf("accepted a finding unrelated to changed lines: %v", err)
	}
	incomplete := []byte(`{"summary":"Source examined; binary omitted","coverage":"incomplete","findings":[],"checks":[],"limitations":["Binary could not be inspected"]}`)
	report, err = ValidateReport(incomplete, ctx)
	if err != nil || Assess(runstate.Succeeded, &report) != AssessmentIncomplete {
		t.Fatalf("incomplete report looked clean: %v", err)
	}
	if Assess(runstate.Succeeded, nil) != AssessmentIncomplete ||
		Assess(runstate.Failed, &report) != AssessmentIncomplete {
		t.Fatal("missing output or failed execution became a completed review")
	}
}

func TestAssessmentNeverPromotesPartialExecution(t *testing.T) {
	clean := &ValidatedReport{Report: Report{Coverage: "complete"}}
	for _, state := range []runstate.Status{runstate.Failed, runstate.Cancelled, runstate.Running} {
		if got := Assess(state, clean); got == AssessmentNoBlocking {
			t.Fatalf("%s promoted partial output", state)
		}
	}
	if Assess(runstate.Succeeded, clean) != AssessmentNoBlocking {
		t.Fatal("clean completion lost")
	}
}
