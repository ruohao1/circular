package delegations

import (
	"encoding/hex"
	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"strings"
	"unicode/utf8"
)

type Checkpoint struct {
	Source       artifacts.Content `json:"source"`
	Progress     artifacts.Content `json:"progress"`
	BaseCommit   string            `json:"base_commit"`
	BaseRef      string            `json:"base_ref"`
	SourceCommit string            `json:"source_commit"`
	InputSHA256  string            `json:"input_sha256"`
}

func (c Checkpoint) Validate(parent uuid.UUID) error {
	if parent == uuid.Nil || c.BaseRef == "" || !utf8.ValidString(c.BaseRef) || strings.ContainsAny(c.BaseRef, "\x00\r\n") {
		return ErrInvalid
	}
	for _, commit := range []string{c.BaseCommit, c.SourceCommit} {
		decoded, err := hex.DecodeString(commit)
		if err != nil || (len(decoded) != 20 && len(decoded) != 32) {
			return ErrInvalid
		}
	}
	for _, digest := range []struct {
		value string
		size  int
	}{{c.InputSHA256, 32}, {c.Source.SHA256, 32}, {c.Progress.SHA256, 32}} {
		decoded, err := hex.DecodeString(digest.value)
		if err != nil || len(decoded) != digest.size {
			return ErrInvalid
		}
	}
	for _, file := range []struct {
		content artifacts.Content
		name    string
	}{{c.Source, "delegation-source.tar"}, {c.Progress, "delegation-progress.json"}} {
		uri, err := artifacts.URI(parent, file.name)
		if err != nil || file.content.URI != uri || file.content.SizeBytes < 0 {
			return ErrInvalid
		}
	}
	return nil
}
