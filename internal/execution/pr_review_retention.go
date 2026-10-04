package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/artifacts"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
)

func (r *Retention) FinalizePRReview(ctx context.Context, id uuid.UUID) error {
	state, err := r.store.Read(ctx, id)
	if err != nil {
		return err
	}
	if state.Kind != runstate.PRReview || state.Status != runstate.Finalizing && !state.Status.Terminal() {
		return postgres.ErrResourceState
	}
	if state.ReviewContextSHA256 == "" {
		if state.Status.Terminal() {
			return nil
		}
		return ErrRetention
	}
	target, err := r.target(state)
	if err != nil {
		return err
	}
	var value prreviews.Context
	var candidate *prreviews.ValidatedReport
	if err := r.store.WithRun(ctx, id, func(run *postgres.RunResources) error {
		var err error
		value, err = run.ReviewContext()
		if err != nil {
			return err
		}
		candidate, err = run.ReviewCandidate()
		return err
	}); err != nil {
		return err
	}
	directory := filepath.Join(r.reviewContextRoot, id.String())
	present, err := pathExists(directory)
	if err != nil {
		return err
	}
	var diff []byte
	if present {
		bundle, content, err := prreviews.ReadBundle(directory)
		if err != nil {
			return err
		}
		hash, err := prreviews.Fingerprint(bundle)
		if err != nil || hash != state.ReviewContextSHA256 {
			return ErrRetention
		}
		diff = content
	} else {
		// Cleanup may have removed the input directory before losing its commit
		// acknowledgement. Only already-retained, verified artifacts may recover it.
		found := false
		for _, a := range state.Artifacts {
			if a.ID == artifacts.PRReviewID(id, "pr-review-diff.patch") && a.Kind == "pr_review_diff" {
				if err := r.verify(ctx, id, a); err != nil {
					return err
				}
				diff, err = r.content.ReadBounded(ctx, id, a.URI, prreviews.MaxDiffBytes)
				if err != nil {
					return err
				}
				found = true
			}
		}
		if !found {
			return ErrRetention
		}
	}
	sum := sha256.Sum256(diff)
	if hex.EncodeToString(sum[:]) != value.DiffSHA256 {
		return ErrRetention
	}
	raw, err := prreviews.CanonicalJSON(value)
	if err != nil {
		return err
	}
	files := []struct {
		name    string
		content []byte
	}{{"pr-review-context.json", raw}, {"pr-review-diff.patch", diff}}
	if candidate != nil {
		raw, err = prreviews.CanonicalJSON(candidate.Report)
		if err != nil {
			return err
		}
		files = append(files, struct {
			name    string
			content []byte
		}{"pr-review-report.json", raw})
	}
	for _, file := range files {
		content, err := r.content.Write(ctx, id, file.name, file.content)
		if err != nil {
			return err
		}
		if err = r.content.Verify(ctx, id, content); err != nil {
			return err
		}
		if err = r.store.WithRun(ctx, id, func(run *postgres.RunResources) error {
			_, err := run.PersistReviewArtifact(target, file.name, content)
			return err
		}); err != nil {
			return err
		}
	}
	return nil
}
func (r *Retention) removeReviewContext(ctx context.Context, state postgres.ResourceState) error {
	directory := filepath.Join(r.reviewContextRoot, state.RunID.String())
	present, err := pathExists(directory)
	if err != nil || !present {
		return err
	}
	value, _, err := prreviews.ReadBundle(directory)
	if err != nil || value.RunID != state.RunID {
		return ErrRetention
	}
	if state.ReviewContextSHA256 != "" {
		hash, err := prreviews.Fingerprint(value)
		if err != nil || hash != state.ReviewContextSHA256 {
			return ErrRetention
		}
		for _, name := range []string{"pr-review-context.json", "pr-review-diff.patch"} {
			found := false
			for _, a := range state.Artifacts {
				if a.ID == artifacts.PRReviewID(state.RunID, name) {
					if err := r.verify(ctx, state.RunID, a); err != nil {
						return err
					}
					found = true
				}
			}
			if !found {
				return ErrRetention
			}
		}
	}
	root, err := os.OpenRoot(r.reviewContextRoot)
	if err != nil {
		return ErrRetention
	}
	defer root.Close()
	if err := root.RemoveAll(state.RunID.String()); err != nil {
		return ErrRetention
	}
	dir, err := root.Open(".")
	if err != nil {
		return ErrRetention
	}
	defer dir.Close()
	return dir.Sync()
}
