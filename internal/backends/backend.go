// Package backends translates backend-specific invocations and output into
// Circular events. Runtime allocation and output framing stay with execution.
package backends

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runstate"
	"github.com/ruohao1/circular/internal/runtimes"
)

// MaxLineBytes bounds a single backend output record before decoding.
const MaxLineBytes = 1024 * 1024

type Input struct {
	RunID               uuid.UUID
	Kind                runstate.Kind
	ReviewContextSHA256 string
	TaskTitle           string
	TaskDescription     string
	Instructions        string
	Config              json.RawMessage
}

// Event carries both the normalized projection and the original backend record.
type Event struct {
	Type   string
	Source string
	Data   map[string]any
	Raw    map[string]any
}

type Backend interface {
	Prepare(Input) (Invocation, error)
}

// Invocation is prepared independently for each Run. Its decoder may retain
// protocol state, so it must not be shared across executions.
type Invocation struct {
	Command        []string
	Stdin          []byte
	Decoder        Decoder
	NetworkEnabled bool
	UseCredentials bool
	// StderrText marks an unstructured diagnostic stream that is discarded.
	StderrText         bool
	TemporaryStorageMB int64
}

type Decoder interface {
	Decode([]byte, runtimes.Stream, int) ([]Event, error)
	Finish() error
}

// Failure preserves a safe public message and, when available, the failing
// backend record. Cause retains diagnostics without projecting them publicly.
type Failure struct {
	Message string
	Raw     map[string]any
	Cause   error
}

func (e *Failure) Error() string { return e.Message }
func (e *Failure) Unwrap() error { return e.Cause }

// ReportedFailure is a validated failure record from structured stderr. Pending
// stdout may still contain progress emitted before that diagnostic.
type ReportedFailure struct{ *Failure }

func (e *ReportedFailure) Unwrap() error { return e.Failure }
