package prreviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ruohao1/circular/internal/runstate"
)

func cleanReport() Report {
	return Report{Summary: "Examined changes", Coverage: "complete", Findings: []Finding{}, Checks: []Check{}, Limitations: []string{}}
}
func encodeReport(t *testing.T, r Report) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReportEnforcesByteCollectionAndTextBounds(t *testing.T) {
	raw := encodeReport(t, cleanReport())
	padded := append(bytes.Clone(raw), bytes.Repeat([]byte(" "), MaxReportBytes-len(raw))...)
	if _, err := ValidateReport(padded, Context{}); err != nil {
		t.Fatal("exact report byte boundary rejected", err)
	}
	if _, err := ValidateReport(append(padded, ' '), Context{}); !errors.Is(err, ErrReportLimit) {
		t.Fatal("oversize report accepted", err)
	}
	for name, change := range map[string]func(*Report){
		"findings":                func(r *Report) { r.Findings = make([]Finding, 51) },
		"checks":                  func(r *Report) { r.Checks = make([]Check, 51) },
		"limitations":             func(r *Report) { r.Limitations = make([]string, 51) },
		"summary":                 func(r *Report) { r.Summary = strings.Repeat("界", 8001) },
		"coverage":                func(r *Report) { r.Coverage = "approved" },
		"missing limitations":     func(r *Report) { r.Coverage = "incomplete" },
		"not-run reason":          func(r *Report) { r.Checks = []Check{{Method: "go test", Outcome: "not_run"}} },
		"passed without evidence": func(r *Report) { r.Checks = []Check{{Method: "go test", Outcome: "passed"}} },
	} {
		t.Run(name, func(t *testing.T) {
			r := cleanReport()
			change(&r)
			if _, err := ValidateReport(encodeReport(t, r), Context{}); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	r := cleanReport()
	r.Summary = strings.Repeat("界", 8000)
	for range 50 {
		r.Checks = append(r.Checks, Check{Method: "test", Outcome: "not_run", NotRunReason: "No test dependencies"})
	}
	if _, err := ValidateReport(encodeReport(t, r), Context{}); err != nil {
		t.Fatal("exact count/text limit rejected", err)
	}
}

func TestReportRejectsAmbiguousJSONAndSourceIdentity(t *testing.T) {
	raw := encodeReport(t, cleanReport())
	for _, data := range [][]byte{
		append(bytes.Clone(raw), raw...),
		bytes.Replace(raw, []byte(`"summary":`), []byte(`"summary":"earlier","summary":`), 1),
		bytes.Replace(raw, []byte(`"summary":`), []byte(`"run_id":"forged","summary":`), 1),
		bytes.Replace(raw, []byte("Examined changes"), []byte{0xff}, 1),
		bytes.Replace(raw, []byte("Examined changes"), []byte(`\ud800`), 1),
		bytes.Replace(raw, []byte("Examined changes"), []byte(`\udc00`), 1),
	} {
		if _, err := ValidateReport(data, Context{}); !errors.Is(err, ErrInvalidReport) {
			t.Fatalf("ambiguous or forged report accepted: %v", err)
		}
	}
	valid := bytes.Replace(raw, []byte("Examined changes"), []byte(`\ud83d\ude00`), 1)
	if _, err := ValidateReport(valid, Context{}); err != nil {
		t.Fatal("valid surrogate pair rejected", err)
	}
}

func TestReportHandlesDeletedRenamedAndUnsupportedFiles(t *testing.T) {
	ctx := Context{Files: []ChangedFile{
		{OldPath: "old.go", NewPath: "new.go", BaseLines: 3, HeadLines: 4, BaseChanged: []LineRange{{2, 2}}, HeadChanged: []LineRange{{2, 3}}},
		{OldPath: "deleted.go", BaseLines: 3, BaseChanged: []LineRange{{1, 3}}},
	}}
	f := Finding{Severity: "low", Title: "A detail", Path: "new.go", Side: "head", StartLine: 2, EndLine: 3, Evidence: "Source", Consequence: "Impact", SuggestedFix: "Fix"}
	r := cleanReport()
	r.Findings = []Finding{f}
	validated, err := ValidateReport(encodeReport(t, r), ctx)
	if err != nil || Assess(runstate.Succeeded, &validated) != AssessmentNoBlocking {
		t.Fatal("advisory finding blocked completion", err)
	}
	r.Findings[0].Path = "deleted.go"
	r.Findings[0].Side = "base"
	r.Findings[0].StartLine = 1
	r.Findings[0].EndLine = 3
	if _, err := ValidateReport(encodeReport(t, r), ctx); err != nil {
		t.Fatal("deletion finding rejected", err)
	}
	for _, span := range []LineRange{{0, 1}, {3, 2}, {1, 4}} {
		r.Findings[0].StartLine = span.Start
		r.Findings[0].EndLine = span.End
		if _, err := ValidateReport(encodeReport(t, r), ctx); err == nil {
			t.Fatal("invalid line range accepted", span)
		}
	}
	ctx.Files = append(ctx.Files, ChangedFile{NewPath: "opaque.bin", Binary: true})
	validated, err = ValidateReport(encodeReport(t, cleanReport()), ctx)
	if err != nil || validated.Report.Coverage != "incomplete" || len(validated.Report.Limitations) == 0 {
		t.Fatal("unsupported content was silently treated as reviewed", err)
	}
}
