package completion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrPolicy = errors.New("invalid completion policy")
var ErrChecks = errors.New("invalid required checks")
var requirementKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validText(s string, limit int) bool {
	if !utf8.ValidString(s) || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > limit {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}
func ValidatePolicy(p Policy) error {
	if p.Mode != "review_required" && p.Mode != "direct_completion" {
		return fmt.Errorf("%w: choose review_required or direct_completion", ErrPolicy)
	}
	if len(p.Requirements) > 20 {
		return fmt.Errorf("%w: at most 20 requirements", ErrPolicy)
	}
	seen := map[string]bool{}
	for _, r := range p.Requirements {
		if !requirementKey.MatchString(r.Key) || seen[r.Key] || !validText(r.Description, 1000) {
			return fmt.Errorf("%w: requirements need unique keys and descriptions of 1–1000 characters", ErrPolicy)
		}
		seen[r.Key] = true
	}
	return nil
}
func decimalID(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}
func ValidateChecks(c CheckConfig) error {
	if c.Revision < 1 || c.Revision > 9007199254740991 || len(c.Checks) < 1 || len(c.Checks) > 50 {
		return fmt.Errorf("%w: select 1–50 required checks with a valid revision", ErrChecks)
	}
	seen := map[[3]string]bool{}
	for _, v := range c.Checks {
		key := [3]string{v.WorkflowID, v.AppID, v.Name}
		if !decimalID(v.WorkflowID) || !decimalID(v.AppID) || !validText(v.Name, 200) || !validText(v.WorkflowPath, 2048) || !strings.HasPrefix(v.WorkflowPath, ".github/workflows/") || path.Clean(v.WorkflowPath) != v.WorkflowPath || seen[key] {
			return fmt.Errorf("%w: checks need unique workflow/app/job identities and valid workflow paths", ErrChecks)
		}
		seen[key] = true
	}
	return nil
}
func SealContract(p Policy, c CheckConfig) (Contract, error) {
	if err := ValidatePolicy(p); err != nil {
		return Contract{}, err
	}
	if p.Mode == "review_required" {
		if err := ValidateChecks(c); err != nil {
			return Contract{}, err
		}
	} else {
		c = CheckConfig{Checks: []RequiredCheck{}}
	}
	// Copy through JSON so callers cannot mutate a sealed contract's slices.
	raw, err := json.Marshal(Contract{Policy: p, CheckConfig: c})
	if err != nil {
		return Contract{}, err
	}
	var out Contract
	if err = json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	digest := sha256.Sum256(raw)
	out.Fingerprint = hex.EncodeToString(digest[:])
	return out, nil
}
