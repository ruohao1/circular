package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

// Verify checks retained content with bounded memory before destructive cleanup.
// A database Artifact alone is not proof that its bytes remain available.
func (s *LocalStore) Verify(ctx context.Context, runID uuid.UUID, expected Content) error {
	prefix := "artifact://" + runID.String() + "/"
	if !strings.HasPrefix(expected.URI, prefix) || expected.SizeBytes < 0 {
		return ErrContent
	}
	name := strings.TrimPrefix(expected.URI, prefix)
	uri, err := URI(runID, name)
	digest, hexErr := hex.DecodeString(expected.SHA256)
	if err != nil || uri != expected.URI || hexErr != nil || len(digest) != 32 {
		return ErrContent
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	run, err := s.openRun(runID, false)
	if err != nil {
		return ErrContent
	}
	defer run.Close()
	file, err := openContent(run, name)
	if err != nil {
		return ErrContent
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != expected.SizeBytes {
		return ErrContent
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(hash, contextReader{ctx, file}, make([]byte, 1024*1024))
	if err != nil || size != expected.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return errors.Join(ErrContent, ctx.Err())
	}
	return ctx.Err()
}
