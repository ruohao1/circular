package prreviews

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runstate"
)

// internal/prreviews/types.go
type SettingsUpdate struct {
	Automatic  bool       `json:"automatic"`
	ReviewerID *uuid.UUID `json:"reviewer_id,omitempty"` // nil preserves selection
}
type Settings struct {
	Automatic         bool              `json:"automatic"`
	Available         bool              `json:"available"`
	ReviewerID        *uuid.UUID        `json:"reviewer_id"`
	Reviewer          *ReviewerSnapshot `json:"reviewer"`
	UnavailableReason string            `json:"unavailable_reason"`
	PendingCount      int               `json:"pending_count"`
}
type LaunchRequest struct {
	RequestKey               uuid.UUID `json:"request_key"`
	ReviewerID               uuid.UUID `json:"reviewer_id"`
	ExpectedInputFingerprint string    `json:"expected_input_fingerprint"`
	Mode                     string    `json:"mode"`               // normal or again
	PreviousReviewID         uuid.UUID `json:"previous_review_id"` // required for again, otherwise zero
}
type Preparation struct {
	Ready    bool           `json:"ready"`
	Reason   string         `json:"reason"`
	Snapshot LaunchSnapshot `json:"snapshot"`
}
type Freshness struct {
	Status          string     `json:"status"`
	PRState         string     `json:"pr_state"`
	ObservedBaseSHA string     `json:"observed_base_sha"`
	ObservedHeadSHA string     `json:"observed_head_sha"`
	Error           string     `json:"error"`
	CheckedAt       *time.Time `json:"checked_at"`
}
type Publication struct {
	Publisher json.RawMessage `json:"publisher,omitempty"`
	Status    string          `json:"status"`
	URL       string          `json:"url"`
	Error     string          `json:"error"`
	Retryable bool            `json:"retryable"`
}
type Review struct {
	ID               uuid.UUID       `json:"id"`
	RunID            uuid.UUID       `json:"run_id"`
	PreviousReviewID *uuid.UUID      `json:"previous_review_id"`
	Attempt          int             `json:"attempt"`
	Automatic        bool            `json:"automatic"`
	Snapshot         LaunchSnapshot  `json:"snapshot"`
	MergeBaseSHA     string          `json:"merge_base_sha"`
	RunStatus        runstate.Status `json:"run_status"`
	Assessment       Assessment      `json:"assessment"`
	Report           *Report         `json:"report"`
	ReportError      string          `json:"report_error"`
	Freshness        Freshness       `json:"freshness"`
	GitHub           Publication     `json:"github"`
	Linear           Publication     `json:"linear"`
	CreatedAt        time.Time       `json:"created_at"`
}
type Page struct {
	Items      []Review `json:"items"`
	NextCursor string   `json:"next_cursor"`
}
