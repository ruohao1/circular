package delegations

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func fixtureRequest() Request {
	return Request{AgentID: uuid.MustParse("00000000-0000-4000-8000-000000000001"), RequestKey: "review-auth", Objective: DelegationObjective{Title: "Review authentication", Description: "Find bypasses and report evidence."}}
}

func TestDelegationRequestStrictBounds(t *testing.T) {
	valid := fixtureRequest()
	valid.RequestKey = " " + strings.Repeat("界", 200) + " "
	valid.Objective.Title = " " + strings.Repeat("界", 500) + " "
	valid.Objective.Description = " " + strings.Repeat("界", 20000) + " "
	raw, _ := json.Marshal(valid)
	got, err := Decode(raw)
	if err != nil || strings.HasPrefix(got.Objective.Title, " ") || len([]rune(got.Objective.Description)) != 20000 {
		t.Fatal("valid Unicode limits rejected", err)
	}
	for name, change := range map[string]func(*Request){
		"no agent":          func(r *Request) { r.AgentID = uuid.Nil },
		"empty title":       func(r *Request) { r.Objective.Title = " \t" },
		"long title":        func(r *Request) { r.Objective.Title = strings.Repeat("界", 501) },
		"empty description": func(r *Request) { r.Objective.Description = " " },
		"long description":  func(r *Request) { r.Objective.Description = strings.Repeat("x", 20001) },
		"empty key":         func(r *Request) { r.RequestKey = "\t" },
		"long key":          func(r *Request) { r.RequestKey = strings.Repeat("x", 201) },
		"NUL":               func(r *Request) { r.Objective.Description = "a\x00b" },
		"UTF8":              func(r *Request) { r.Objective.Description = string([]byte{255}) },
	} {
		t.Run(name, func(t *testing.T) {
			r := fixtureRequest()
			change(&r)
			if _, err := Normalize(r); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	base, _ := json.Marshal(fixtureRequest())
	for _, bad := range []string{
		string(base) + ` {}`, strings.Replace(string(base), `"title":`, `"title":"duplicate","title":`, 1),
		strings.Replace(string(base), `"objective":`, `"parent_run_id":"forged","objective":`, 1),
		strings.Replace(string(base), `"description":`, `"command":"injected","description":`, 1),
		strings.Replace(string(base), `"Review authentication"`, `"\ud800"`, 1),
		strings.Repeat(" ", 128*1024) + string(base), string([]byte{255}), `null`,
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatal("malformed input accepted")
		}
	}
}

func TestDelegationFingerprintNormalizesOnce(t *testing.T) {
	a := fixtureRequest()
	b := a
	b.Objective.Title = "  " + b.Objective.Title + "  "
	a, _ = Normalize(a)
	b, _ = Normalize(b)
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("whitespace created a new request")
	}
	b.Objective.Description += " More work."
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("different objective has same fingerprint")
	}
}
