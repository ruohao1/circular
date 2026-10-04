package prreviews

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runstate"
)

const (
	MaxDiffBytes       = 32 << 20
	MaxContextBytes    = 8 << 20
	MaxFiles           = 1000
	MaxReportBytes     = 256 << 10
	MaxFindings        = 50
	MaxChecks          = 50
	MaxGitHubBodyRunes = 30000
	ExecutionLimit     = 15 * time.Minute
	FreshnessInterval  = time.Minute
)

type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}
type ChangedFile struct {
	OldPath     string      `json:"old_path"`
	NewPath     string      `json:"new_path"`
	Status      string      `json:"status"`
	BaseLines   int         `json:"base_lines"`
	HeadLines   int         `json:"head_lines"`
	BaseChanged []LineRange `json:"base_changed"`
	HeadChanged []LineRange `json:"head_changed"`
	Binary      bool        `json:"binary"`
	Submodule   bool        `json:"submodule"`
}
type PRIdentity struct {
	InstallationID     string `json:"installation_id"`
	GitHubRepositoryID string `json:"github_repository_id"`
	RepositoryName     string `json:"repository_name"`
	Number             int    `json:"number"`
	URL                string `json:"url"`
	BaseRef            string `json:"base_ref"`
	HeadRef            string `json:"head_ref"`
	BaseSHA            string `json:"base_sha"`
	HeadSHA            string `json:"head_sha"`
}
type ReviewerSnapshot struct {
	AgentID         uuid.UUID       `json:"agent_id"`
	Name            string          `json:"name"`
	Backend         string          `json:"backend"`
	Instructions    string          `json:"instructions"`
	Model           string          `json:"model"`
	ReasoningEffort string          `json:"reasoning_effort"`
	BackendConfig   json.RawMessage `json:"backend_config"`
	PromptVersion   string          `json:"prompt_version"`
	Fingerprint     string          `json:"fingerprint"`
}
type LaunchSnapshot struct {
	SourceRunID      uuid.UUID        `json:"source_run_id"`
	TaskID           uuid.UUID        `json:"task_id"`
	ProjectID        uuid.UUID        `json:"project_id"`
	RepositoryID     uuid.UUID        `json:"repository_id"`
	CloneURL         string           `json:"clone_url"`
	PR               PRIdentity       `json:"pr"`
	Reviewer         ReviewerSnapshot `json:"reviewer"`
	TaskTitle        string           `json:"task_title"`
	TaskDescription  string           `json:"task_description"`
	TaskExternalRefs json.RawMessage  `json:"task_external_refs"`
	Evidence         []Evidence       `json:"evidence"`
	InputFingerprint string           `json:"input_fingerprint"`
}
type Evidence struct {
	SourceRunID   uuid.UUID `json:"source_run_id"`
	ArtifactID    uuid.UUID `json:"artifact_id"`
	EventSequence int64     `json:"event_sequence"`
	Kind          string    `json:"kind"`
	SHA256        string    `json:"sha256"`
	Text          string    `json:"text"`
	Limitation    string    `json:"limitation"`
}
type Context struct {
	ReviewID     uuid.UUID      `json:"review_id"`
	RunID        uuid.UUID      `json:"run_id"`
	Snapshot     LaunchSnapshot `json:"snapshot"`
	MergeBaseSHA string         `json:"merge_base_sha"`
	DiffSHA256   string         `json:"diff_sha256"`
	Files        []ChangedFile  `json:"files"`
	Evidence     []Evidence     `json:"evidence"`
	Limitations  []string       `json:"limitations"`
}
type Finding struct {
	Severity     string `json:"severity"`
	Title        string `json:"title"`
	Path         string `json:"path"`
	Side         string `json:"side"`
	StartLine    int    `json:"start_line"`
	EndLine      int    `json:"end_line"`
	Evidence     string `json:"evidence"`
	Consequence  string `json:"consequence"`
	SuggestedFix string `json:"suggested_fix"`
}
type Check struct {
	Method       string `json:"method"`
	Outcome      string `json:"outcome"`
	Evidence     string `json:"evidence"`
	NotRunReason string `json:"not_run_reason"`
}
type Report struct {
	Summary     string    `json:"summary"`
	Coverage    string    `json:"coverage"`
	Findings    []Finding `json:"findings"`
	Checks      []Check   `json:"checks"`
	Limitations []string  `json:"limitations"`
}
type ValidatedReport struct {
	Report Report
	SHA256 string
}
type Assessment string

const (
	AssessmentPending    Assessment = "pending"
	AssessmentFindings   Assessment = "findings"
	AssessmentNoBlocking Assessment = "no_blocking_findings"
	AssessmentIncomplete Assessment = "incomplete"
)

func Assess(status runstate.Status, report *ValidatedReport) Assessment {
	if status != runstate.Succeeded || report == nil || report.Report.Coverage != "complete" {
		return AssessmentIncomplete
	}
	for _, finding := range report.Report.Findings {
		if finding.Severity != "low" {
			return AssessmentFindings
		}
	}
	return AssessmentNoBlocking
}

type LinearSummary struct {
	ReviewID    uuid.UUID  `json:"review_id"`
	RunID       uuid.UUID  `json:"run_id"`
	Assessment  Assessment `json:"assessment"`
	HeadSHA     string     `json:"head_sha"`
	Critical    int        `json:"critical"`
	High        int        `json:"high"`
	Medium      int        `json:"medium"`
	Low         int        `json:"low"`
	ReportError string     `json:"report_error"`
	GitHubURL   string     `json:"github_url"`
}
