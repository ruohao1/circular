package completion

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestCompletionReportBounds(t *testing.T) {
	p := Policy{Mode: "direct_completion", Requirements: []Requirement{{Key: "tests", Description: "Test evidence"}}}
	valid := Report{Result: "Implemented", Evidence: []Evidence{{Key: "tests", Detail: "Passed", Links: []string{"https://example.org/tests"}}}}
	if err := ValidateReport(p, valid); err != nil {
		t.Fatal(err)
	}
	largestPR := 2147483647
	valid.PullRequestNumber = &largestPR
	if err := ValidateReport(p, valid); err != nil {
		t.Fatal("largest supported PR number rejected", err)
	}
	cases := []struct {
		name   string
		change func(*Report)
	}{
		{"empty", func(r *Report) { r.Result = " " }}, {"too long", func(r *Report) { r.Result = strings.Repeat("é", 4001) }},
		{"missing", func(r *Report) { r.Evidence = nil }}, {"unknown", func(r *Report) { r.Evidence[0].Key = "unknown" }},
		{"duplicate", func(r *Report) { r.Evidence = append(r.Evidence, r.Evidence[0]) }},
		{"no detail", func(r *Report) { r.Evidence[0].Detail = "" }}, {"utf8", func(r *Report) { r.Result = "\xff" }},
		{"control", func(r *Report) { r.Evidence[0].Detail = "ok\x1b" }},
		{"http", func(r *Report) { r.Evidence[0].Links = []string{"http://example.org"} }},
		{"credentials", func(r *Report) { r.Evidence[0].Links = []string{"https://user:pass@example.org"} }},
		{"no host", func(r *Report) { r.Evidence[0].Links = []string{"https:///path"} }},
		{"url control", func(r *Report) { r.Evidence[0].Links = []string{"https://example.org/%0a"} }},
		{"url long", func(r *Report) { r.Evidence[0].Links = []string{"https://example.org/" + strings.Repeat("a", 2048)} }},
		{"too many links", func(r *Report) { r.Evidence[0].Links = slices.Repeat([]string{"https://example.org"}, 6) }},
		{"nonpositive PR", func(r *Report) { n := 0; r.PullRequestNumber = &n }},
		{"oversized PR", func(r *Report) { n := 2147483648; r.PullRequestNumber = &n }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := valid
			r.Evidence = slices.Clone(valid.Evidence)
			c.change(&r)
			if ValidateReport(p, r) == nil {
				t.Fatal("bad report accepted")
			}
		})
	}
	big := Report{Result: "done"}
	p.Requirements = nil
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("e%d", i)
		p.Requirements = append(p.Requirements, Requirement{Key: key, Description: "Required"})
		big.Evidence = append(big.Evidence, Evidence{Key: key, Detail: strings.Repeat("界", 4000)})
	}
	if ValidateReport(p, big) == nil {
		t.Fatal("oversized JSON report accepted")
	}
}
