package prreviews

import (
	"testing"
	"time"
)

func TestFreshnessIsIndependentOfAssessment(t *testing.T) {
	now := time.Now()
	captured := PRIdentity{BaseSHA: "base", HeadSHA: "head"}
	for _, tc := range []struct {
		name, base, head, state, want string
		checked                       *time.Time
	}{
		{"unknown", "", "", "unknown", "unknown", nil},
		{"current", "base", "head", "open", "current", &now},
		{"head moved", "base", "next", "open", "outdated", &now},
		{"base moved", "next", "head", "open", "outdated", &now},
		{"closed", "base", "head", "closed", "current", &now},
		{"merged", "base", "head", "merged", "current", &now},
		{"unavailable", "base", "head", "unavailable", "unavailable", &now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ProjectFreshness(captured, tc.base, tc.head, tc.state, tc.checked, "")
			if got.Status != tc.want || got.PRState != tc.state {
				t.Fatalf("got %+v", got)
			}
		})
	}
}
