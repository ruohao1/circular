package execution

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	git "github.com/ruohao1/circular/internal/git"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

func (s *Supervisor) preparePRReviewContext(ctx context.Context, inputs postgres.ProvisioningContext) (git.ReviewSource, prreviews.Context, error) {
	var source git.ReviewSource
	var value prreviews.Context
	if inputs.Kind != runstate.PRReview || inputs.Review == nil || inputs.ReviewID == uuid.Nil || s.config.ReviewContextRoot == "" {
		return source, value, prreviews.ErrSourceInvalid
	}
	snapshot := *inputs.Review
	value = prreviews.Context{ReviewID: inputs.ReviewID, RunID: inputs.RunID, Snapshot: snapshot, Evidence: append([]prreviews.Evidence{}, snapshot.Evidence...)}
	var records []artifacts.Record
	if err := s.store.WithRun(ctx, inputs.RunID, func(r *postgres.RunResources) error {
		var err error
		records, err = r.ReviewEvidenceRecords()
		return err
	}); err != nil {
		return source, value, err
	}
	for _, record := range records {
		raw, err := json.Marshal(record.Metadata)
		if err != nil {
			return source, value, prreviews.ErrSourceInvalid
		}
		var meta struct {
			Size   int64  `json:"size_bytes"`
			SHA256 string `json:"sha256"`
		}
		if json.Unmarshal(raw, &meta) != nil || meta.Size < 0 || meta.Size > prreviews.MaxDiffBytes {
			return source, value, prreviews.ErrSourceInvalid
		}
		if err := s.retention.content.Verify(ctx, record.RunID, artifacts.Content{URI: record.URI, SizeBytes: meta.Size, SHA256: meta.SHA256}); err != nil {
			return source, value, prreviews.ErrSourceInvalid
		}
		content, err := s.retention.content.ReadBounded(ctx, record.RunID, record.URI, prreviews.MaxDiffBytes)
		if err != nil {
			return source, value, prreviews.ErrSourceInvalid
		}
		for i := range value.Evidence {
			e := &value.Evidence[i]
			if e.ArtifactID != record.ID {
				continue
			}
			e.Limitation = "Prior coding-run artifact, verified against its retained checksum; it is not a check rerun by the reviewer."
			if len(content) > 64<<10 {
				content = content[:64<<10]
				e.Limitation += " Only the first 64 KiB is included here; diff.patch is the authoritative full PR comparison."
			}
			e.Text = strings.ToValidUTF8(string(content), "�")
		}
	}
	if len(value.Evidence) == 0 {
		value.Evidence = append(value.Evidence, prreviews.Evidence{SourceRunID: snapshot.SourceRunID, Kind: "prior_checks", Limitation: "No retained coding-run test evidence is available; report separately any checks you can run."})
	}
	source, err := s.retention.git.PreparePRReview(ctx, inputs.RunID, inputs.RepositoryID, inputs.CloneURL, snapshot.PR)
	if err != nil {
		return source, value, err
	}
	value.MergeBaseSHA = source.MergeBaseSHA
	value.DiffSHA256 = source.DiffSHA256
	value.Files = source.Files
	value.Limitations = source.Limitations
	digest, err := prreviews.Fingerprint(value)
	if err != nil {
		return source, value, err
	}
	err = s.withAllocation(ctx, inputs, func(operation context.Context, r *postgres.RunResources) error {
		if err := operation.Err(); err != nil {
			return err
		}
		if err := prreviews.WriteContext(filepath.Join(s.config.ReviewContextRoot, inputs.RunID.String()), value, source.Diff); err != nil {
			return err
		}
		return r.PersistReviewContext(value, digest)
	})
	return source, value, err
}
