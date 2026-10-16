package artifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/google/uuid"
)

// Verify checks retained content with bounded memory before destructive cleanup.
// A database Artifact alone is not proof that its bytes remain available.
func (s *LocalStore) Verify(ctx context.Context, runID uuid.UUID, expected Content) error {
	file, err := s.OpenVerified(ctx, runID, expected)
	if err != nil {
		return err
	}
	return file.Close()
}

// OpenVerified returns the verified file at offset zero for bounded streaming.
func (s *LocalStore) OpenVerified(ctx context.Context, runID uuid.UUID, expected Content) (file *os.File, result error) {
	prefix := "artifact://" + runID.String() + "/"
	if !strings.HasPrefix(expected.URI, prefix) || expected.SizeBytes < 0 {
		return nil, ErrContent
	}
	name := strings.TrimPrefix(expected.URI, prefix)
	uri, err := URI(runID, name)
	digest, hexErr := hex.DecodeString(expected.SHA256)
	if err != nil || uri != expected.URI || hexErr != nil || len(digest) != 32 {
		return nil, ErrContent
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	run, err := s.openRun(runID, false)
	if err != nil {
		return nil, ErrContent
	}
	defer run.Close()
	file, err = openContent(run, name)
	if err != nil {
		return nil, ErrContent
	}
	opened := file
	defer func() {
		if result != nil {
			_ = opened.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || info.Size() != expected.SizeBytes {
		return nil, ErrContent
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(hash, contextReader{ctx, file}, make([]byte, 1024*1024))
	if err != nil || size != expected.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return nil, errors.Join(ErrContent, ctx.Err())
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return file, nil
}
