package prreviews

import (
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/codexconfig"
)

const PromptVersion = "pr-review-v1"

var DigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var CommitPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func NormalizeReviewer(value ReviewerSnapshot) (ReviewerSnapshot, error) {
	if value.AgentID == uuid.Nil || value.Backend != "codex" {
		return value, ErrReviewUnavailable
	}
	var settings codexconfig.Settings
	if err := DecodeStrict(value.BackendConfig, &settings); err != nil {
		return value, fmt.Errorf("%w: invalid reviewer model settings", ErrReviewUnavailable)
	}
	settings, err := codexconfig.Resolve(settings.Model, settings.ReasoningEffort)
	if err != nil {
		return value, fmt.Errorf("%w: %s", ErrReviewUnavailable, err)
	}
	value.Model, value.ReasoningEffort = settings.Model, settings.ReasoningEffort
	value.BackendConfig, err = json.Marshal(settings)
	if err != nil {
		return value, err
	}
	value.PromptVersion = PromptVersion
	value.Fingerprint = ""
	value.Fingerprint, err = Fingerprint(value)
	return value, err
}

func SealSnapshot(value LaunchSnapshot) (LaunchSnapshot, error) {
	var err error
	value.Reviewer, err = NormalizeReviewer(value.Reviewer)
	if err != nil {
		return value, err
	}
	if value.Evidence == nil {
		value.Evidence = []Evidence{}
	}
	if len(value.TaskExternalRefs) == 0 {
		value.TaskExternalRefs = json.RawMessage(`{}`)
	}
	value.InputFingerprint = ""
	value.InputFingerprint, err = Fingerprint(value)
	return value, err
}

func (request LaunchRequest) Validate() error {
	if request.RequestKey == uuid.Nil || !DigestPattern.MatchString(request.ExpectedInputFingerprint) {
		return fmt.Errorf("%w: request key and prepared input fingerprint are required", ErrRequestConflict)
	}
	if request.Mode != "normal" && request.Mode != "again" || request.Mode == "again" && request.PreviousReviewID == uuid.Nil || request.Mode == "normal" && request.PreviousReviewID != uuid.Nil {
		return fmt.Errorf("%w: choose normal, or again with a previous review", ErrRequestConflict)
	}
	return nil
}

func ReviewIdentity(snapshot LaunchSnapshot) (string, error) {
	return Fingerprint(struct {
		Repository           uuid.UUID
		GitHubRepository     string
		Number               int
		Base, Head, Reviewer string
	}{snapshot.RepositoryID, snapshot.PR.GitHubRepositoryID, snapshot.PR.Number, snapshot.PR.BaseSHA, snapshot.PR.HeadSHA, snapshot.Reviewer.Fingerprint})
}
