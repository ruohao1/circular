package completion

import (
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/prreviews"
)

const EvidenceTTL = time.Minute

type CheckSetup struct {
	RepositoryName string          `json:"repository_name"`
	Config         *CheckConfig    `json:"config"`
	Available      []RequiredCheck `json:"available"`
	Reason         string          `json:"reason"`
}

type Binding struct {
	Number   int   `json:"number"`
	Revision int64 `json:"revision"`
}
type ProviderIdentity struct {
	ID             uuid.UUID `json:"id"`
	Generation     int64     `json:"generation"`
	InstallationID string    `json:"installation_id"`
	RepositoryID   string    `json:"repository_id"`
}
type VerifiedRevision struct {
	BaseSHA   string `json:"base_sha"`
	HeadSHA   string `json:"head_sha"`
	TestedSHA string `json:"tested_sha"`
	TreeSHA   string `json:"tree_sha"`
}
type CheckEvidence struct {
	Check         RequiredCheck `json:"check"`
	RunID         string        `json:"run_id"`
	Attempt       int64         `json:"attempt"`
	JobID         string        `json:"job_id"`
	CheckRunID    string        `json:"check_run_id"`
	TestedSHA     string        `json:"tested_sha"`
	RunStatus     string        `json:"run_status"`
	RunConclusion string        `json:"run_conclusion"`
	Status        string        `json:"status"`
	Conclusion    string        `json:"conclusion"`
	URL           string        `json:"url"`
}
type ReviewEvidence struct {
	ID                  uuid.UUID           `json:"id"`
	RunID               uuid.UUID           `json:"run_id"`
	State               string              `json:"state"`
	BaseSHA             string              `json:"base_sha"`
	HeadSHA             string              `json:"head_sha"`
	ReviewerFingerprint string              `json:"reviewer_fingerprint"`
	ReportSHA           string              `json:"report_sha"`
	Coverage            string              `json:"coverage"`
	Retained            bool                `json:"retained"`
	Findings            []prreviews.Finding `json:"findings"`
}
type Observation struct {
	SessionID           uuid.UUID            `json:"session_id"`
	BindingRevision     int64                `json:"binding_revision"`
	ReviewRevision      int64                `json:"review_revision"`
	ContractFingerprint string               `json:"contract_fingerprint"`
	Identity            ProviderIdentity     `json:"identity"`
	ReviewID            uuid.UUID            `json:"review_id"`
	ReviewerFingerprint string               `json:"reviewer_fingerprint"`
	ReviewReportSHA     string               `json:"review_report_sha"`
	PR                  prreviews.PRIdentity `json:"pr"`
	PRState             string               `json:"pr_state"`
	Revision            VerifiedRevision     `json:"revision"`
	Checks              []CheckEvidence      `json:"checks"`
	Merged              bool                 `json:"merged"`
	MergeSHA            string               `json:"merge_sha"`
	MergeVerified       bool                 `json:"merge_verified"`
	ErrorCode           string               `json:"error_code"`
	CheckedAt           time.Time            `json:"checked_at"`
}

func (r VerifiedRevision) Valid() bool {
	return prreviews.CommitPattern.MatchString(r.BaseSHA) && prreviews.CommitPattern.MatchString(r.HeadSHA) && prreviews.CommitPattern.MatchString(r.TestedSHA) && prreviews.CommitPattern.MatchString(r.TreeSHA)
}
