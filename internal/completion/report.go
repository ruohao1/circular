package completion

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxReportBytes = 128 * 1024

var ErrReport = errors.New("invalid completion report")

func ValidateReport(p Policy, r Report) error {
	if err := ValidatePolicy(p); err != nil {
		return err
	}
	if !validText(r.Result, 4000) || r.PullRequestNumber != nil && *r.PullRequestNumber <= 0 {
		return fmt.Errorf("%w: supply a result of 1–4000 characters and a positive PR number", ErrReport)
	}
	required := map[string]bool{}
	for _, v := range p.Requirements {
		required[v.Key] = true
	}
	for _, v := range r.Evidence {
		if !required[v.Key] || !validText(v.Detail, 4000) || len(v.Links) > 5 {
			return fmt.Errorf("%w: evidence needs unique required keys, detail, and at most five links", ErrReport)
		}
		delete(required, v.Key)
		for _, link := range v.Links {
			u, err := url.Parse(link)
			decoded, de := url.PathUnescape(link)
			if err != nil || de != nil || !validText(link, 2048) || !utf8.ValidString(decoded) || strings.ContainsFunc(decoded, unicode.IsControl) || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
				return fmt.Errorf("%w: links must be HTTPS URLs without credentials or control characters", ErrReport)
			}
		}
	}
	if len(required) != 0 {
		return fmt.Errorf("%w: supply evidence for every requirement", ErrReport)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > MaxReportBytes {
		return fmt.Errorf("%w: report exceeds 128 KiB", ErrReport)
	}
	return nil
}
