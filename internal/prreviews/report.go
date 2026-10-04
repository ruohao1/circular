// Package prreviews defines trusted review inputs and validates untrusted assessments.
package prreviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidReport     = errors.New("review report has invalid fields or source locations")
	ErrReportLimit       = errors.New("review report exceeds its supported size or item limits")
	ErrConflictingReport = errors.New("a different report was already submitted for this review")
	ErrRequestConflict   = errors.New("this request key was already used with different review parameters")
	ErrReviewUnavailable = errors.New("the source PR or selected reviewer is unavailable")
	ErrSnapshotChanged   = errors.New("the PR or reviewer changed; refresh the review preparation")
	ErrSourceInvalid     = errors.New("the captured review source could not be verified")
)

func ValidateReport(raw []byte, context Context) (ValidatedReport, error) {
	return validateReport(raw, &context)
}

// DecodeReport checks untrusted structure and bounds before the worker repeats
// validation against its retained source manifest.
func DecodeReport(raw []byte) (Report, error) {
	value, err := validateReport(raw, nil)
	return value.Report, err
}

func validateReport(raw []byte, context *Context) (ValidatedReport, error) {
	if len(raw) > MaxReportBytes {
		return ValidatedReport{}, ErrReportLimit
	}
	var report Report
	if err := DecodeStrict(raw, &report); err != nil {
		return ValidatedReport{}, ErrInvalidReport
	}
	if len(report.Findings) > MaxFindings || len(report.Checks) > MaxChecks || len(report.Limitations) > 50 {
		return ValidatedReport{}, ErrReportLimit
	}
	if !textOK(report.Summary, 8000, true) || !slices.Contains([]string{"complete", "incomplete"}, report.Coverage) || report.Findings == nil || report.Checks == nil || report.Limitations == nil {
		return ValidatedReport{}, ErrInvalidReport
	}
	for _, f := range report.Findings {
		if !slices.Contains([]string{"critical", "high", "medium", "low"}, f.Severity) || !textOK(f.Title, 200, true) || !textOK(f.Evidence, 8000, true) || !textOK(f.Consequence, 8000, true) || !textOK(f.SuggestedFix, 8000, true) {
			return ValidatedReport{}, ErrInvalidReport
		}
		if !SafePath(f.Path) || (f.Side != "base" && f.Side != "head") || f.StartLine < 1 || f.EndLine < f.StartLine {
			return ValidatedReport{}, ErrInvalidReport
		}
		if context != nil {
			matches := 0
			for _, file := range context.Files {
				if validLocation(f, file) {
					matches++
				}
			}
			if matches != 1 {
				return ValidatedReport{}, ErrInvalidReport
			}
		}
	}
	for _, c := range report.Checks {
		if !textOK(c.Method, 8000, true) || !slices.Contains([]string{"passed", "failed", "not_run"}, c.Outcome) || !textOK(c.Evidence, 8000, c.Outcome != "not_run") || !textOK(c.NotRunReason, 8000, c.Outcome == "not_run") {
			return ValidatedReport{}, ErrInvalidReport
		}
	}
	for _, limitation := range report.Limitations {
		if !textOK(limitation, 2000, true) {
			return ValidatedReport{}, ErrInvalidReport
		}
	}
	if report.Coverage == "incomplete" && len(report.Limitations) == 0 {
		return ValidatedReport{}, ErrInvalidReport
	}
	limitations := []string{}
	if context != nil {
		limitations = slices.Clone(context.Limitations)
		for _, file := range context.Files {
			if file.Binary || file.Submodule {
				name := file.NewPath
				if name == "" {
					name = file.OldPath
				}
				limitations = append(limitations, "Unsupported binary or submodule content: "+name)
			}
		}
	}
	if len(limitations) > 0 {
		report.Coverage = "incomplete"
		for _, limitation := range limitations {
			if !slices.Contains(report.Limitations, limitation) && len(report.Limitations) < 50 {
				runes := []rune(limitation)
				if len(runes) > 2000 {
					runes = runes[:2000]
				}
				report.Limitations = append(report.Limitations, string(runes))
			}
		}
	}
	canonical, err := json.Marshal(report)
	if err != nil || len(canonical) > MaxReportBytes {
		return ValidatedReport{}, ErrReportLimit
	}
	digest, err := Fingerprint(report)
	return ValidatedReport{Report: report, SHA256: digest}, err
}

func textOK(value string, limit int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= limit && (!required || strings.TrimSpace(value) != "")
}

func SafePath(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && !strings.Contains(value, "\\") && !path.IsAbs(value) && path.Clean(value) == value && !strings.HasPrefix(value, "../")
}

func validLocation(f Finding, file ChangedFile) bool {
	if !SafePath(f.Path) {
		return false
	}
	name, lines, changed := file.NewPath, file.HeadLines, file.HeadChanged
	if f.Side == "base" {
		name, lines, changed = file.OldPath, file.BaseLines, file.BaseChanged
	} else if f.Side != "head" {
		return false
	}
	if f.Path != name || file.Binary || file.Submodule || f.StartLine < 1 || f.EndLine < f.StartLine || f.EndLine > lines {
		return false
	}
	for _, span := range changed {
		if f.StartLine <= span.End && span.Start <= f.EndLine {
			return true
		}
	}
	return false
}

// DecodeStrict rejects ambiguous JSON before the typed decoder can discard keys.
func DecodeStrict(raw []byte, target any) error {
	if !utf8.Valid(raw) || !json.Valid(raw) || !validSurrogates(raw) {
		return ErrInvalidReport
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := uniqueValue(d, 0); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return ErrInvalidReport
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return ErrInvalidReport
	}
	return nil
}

func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 20 {
		return ErrInvalidReport
	}
	t, err := d.Token()
	if err != nil {
		return ErrInvalidReport
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		keys := map[string]bool{}
		for d.More() {
			t, err := d.Token()
			k, ok := t.(string)
			if err != nil || !ok || keys[k] {
				return ErrInvalidReport
			}
			keys[k] = true
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalidReport
	}
	_, err = d.Token()
	return err
}

func validSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		var value uint16
		if _, err := fmt.Sscanf(string(raw[i+1:i+5]), "%04x", &value); err != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			var low uint16
			if _, err := fmt.Sscanf(string(raw[i+3:i+7]), "%04x", &low); err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
