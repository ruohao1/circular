package prreviews

import "time"

// ProjectFreshness compares observations without changing the recorded assessment.
func ProjectFreshness(captured PRIdentity, base, head, state string, checked *time.Time, safeError string) Freshness {
	value := Freshness{Status: "unknown", PRState: state, ObservedBaseSHA: base, ObservedHeadSHA: head, CheckedAt: checked, Error: safeError}
	if state == "unavailable" {
		value.Status = "unavailable"
		return value
	}
	if checked == nil || base == "" || head == "" {
		return value
	}
	value.Status = "current"
	if base != captured.BaseSHA || head != captured.HeadSHA {
		value.Status = "outdated"
	}
	return value
}
