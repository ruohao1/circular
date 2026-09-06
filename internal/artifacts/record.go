package artifacts

import "github.com/google/uuid"

// Record is the durable Artifact projection shared with the HTTP API and persisted data.
type Record struct {
	ID       uuid.UUID      `json:"id"`
	RunID    uuid.UUID      `json:"run_id"`
	Kind     string         `json:"kind"`
	URI      string         `json:"uri"`
	Metadata map[string]any `json:"metadata"`
}

func DiffID(runID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("io.circular.artifact:"+runID.String()+":git-diff"))
}
func ArchiveID(runID uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("io.circular.artifact:"+runID.String()+":worktree"))
}
