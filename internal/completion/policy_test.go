package completion

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestCompletionPolicyBounds(t *testing.T) {
	valid := Policy{Mode: "review_required", Requirements: []Requirement{{Key: "tests", Description: "Show test results"}}}
	cases := []struct {
		name   string
		change func(*Policy)
	}{
		{"mode", func(p *Policy) { p.Mode = "auto" }},
		{"too many", func(p *Policy) {
			for i := 0; i < 20; i++ {
				p.Requirements = append(p.Requirements, Requirement{Key: fmt.Sprintf("key_%d", i), Description: "Evidence"})
			}
		}},
		{"duplicate", func(p *Policy) { p.Requirements = append(p.Requirements, p.Requirements[0]) }},
		{"empty description", func(p *Policy) { p.Requirements[0].Description = " " }},
		{"long description", func(p *Policy) { p.Requirements[0].Description = strings.Repeat("é", 1001) }},
		{"invalid key", func(p *Policy) { p.Requirements[0].Key = "Tests" }},
		{"long key", func(p *Policy) { p.Requirements[0].Key = strings.Repeat("a", 65) }},
		{"invalid utf8", func(p *Policy) { p.Requirements[0].Description = "\xff" }},
		{"control", func(p *Policy) { p.Requirements[0].Description = "hello\x00" }},
	}
	for _, mode := range []string{"review_required", "direct_completion"} {
		p := valid
		p.Mode = mode
		if err := ValidatePolicy(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := valid
			p.Requirements = slices.Clone(valid.Requirements)
			c.change(&p)
			if ValidatePolicy(p) == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	p := Policy{Mode: "direct_completion"}
	for i := 0; i < 20; i++ {
		p.Requirements = append(p.Requirements, Requirement{Key: fmt.Sprintf("key_%d", i), Description: strings.Repeat("é", 1000)})
	}
	if err := ValidatePolicy(p); err != nil {
		t.Fatal("exact bounds", err)
	}
	if err := ValidatePolicy(Policy{Mode: "direct_completion"}); err != nil {
		t.Fatal("summary-only policy", err)
	}
}

func TestCompletionPolicySeal(t *testing.T) {
	p := Policy{Mode: "review_required", Requirements: []Requirement{{Key: "tests", Description: "Tests"}}}
	checks := CheckConfig{Revision: 2, Checks: []RequiredCheck{{WorkflowID: "123", AppID: "42", WorkflowPath: ".github/workflows/ci.yml", Name: "Go / PostgreSQL / Docker"}}}
	contract, err := SealContract(p, checks)
	if err != nil || len(contract.Fingerprint) != 64 {
		t.Fatal(contract, err)
	}
	again, err := SealContract(p, checks)
	if err != nil || again.Fingerprint != contract.Fingerprint {
		t.Fatal("unstable contract", err)
	}
	for _, change := range []func(*CheckConfig){
		func(c *CheckConfig) { c.Checks = nil }, func(c *CheckConfig) { c.Revision = 0 },
		func(c *CheckConfig) { c.Checks[0].WorkflowID = "01" }, func(c *CheckConfig) { c.Checks[0].AppID = "-1" },
		func(c *CheckConfig) { c.Checks[0].Name = "" }, func(c *CheckConfig) { c.Checks = append(c.Checks, c.Checks[0]) },
		func(c *CheckConfig) { c.Checks[0].WorkflowPath = "../ci.yml" },
	} {
		c := checks
		c.Checks = slices.Clone(checks.Checks)
		change(&c)
		if _, err := SealContract(p, c); err == nil {
			t.Fatal("bad checks accepted", c)
		}
	}
	p.Mode = "direct_completion"
	if _, err := SealContract(p, CheckConfig{}); err != nil {
		t.Fatal("direct work requires GitHub", err)
	}
}
