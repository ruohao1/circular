package runtimes

import (
	"context"
	"errors"
	"io"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
)

var (
	ErrUnknownHandle  = errors.New("container handle is not owned by this runtime")
	ErrOutputConsumed = errors.New("container output can only be consumed once")
)

// Handle must be retained intact for live operations. Only ResourceID is a
// durable backend identity; ID is adapter-local routing, never cleanup authority.
type Handle struct{ ID, ResourceID string }

type Stream string

const (
	Stdout Stream = "stdout"
	Stderr Stream = "stderr"
)

type Output struct {
	Stream Stream
	Data   []byte
}
type CompletionReason string

const (
	Exited  CompletionReason = "exited"
	Stopped CompletionReason = "stopped"
)

type Result struct {
	Reason   CompletionReason
	ExitCode *int
}

// Output claims the one-shot stream immediately. Iterating delivers chunks in
// observation order, including output buffered before the consumer attached.
// Cancelling an observer does not cancel the execution or other Wait callers.
func (d *Docker) Output(ctx context.Context, handle Handle) (iter.Seq2[Output, error], error) {
	e, err := d.execution(handle)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.outputClaimed {
		e.mu.Unlock()
		return nil, ErrOutputConsumed
	}
	e.outputClaimed = true
	e.mu.Unlock()
	var used atomic.Bool
	return func(yield func(Output, error) bool) {
		if !used.CompareAndSwap(false, true) {
			yield(Output{}, ErrOutputConsumed)
			return
		}
		for {
			chunk, err := e.output.next(ctx)
			if errors.Is(err, io.EOF) {
				return
			}
			if !yield(chunk, err) || err != nil {
				return
			}
		}
	}, nil
}

// Wait observes one immutable completion without consuming output or granting
// ownership. Cancelling this caller does not cancel the Run or other observers.
func (d *Docker) Wait(ctx context.Context, handle Handle) (Result, error) {
	e, err := d.execution(handle)
	if err != nil {
		return Result{}, err
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-e.done:
		result := e.result
		if result.ExitCode != nil {
			code := *result.ExitCode
			result.ExitCode = &code
		}
		return result, e.resultErr
	}
}

type outputQueue struct {
	mu     sync.Mutex
	items  []Output
	head   int
	closed bool
	wake   chan struct{}
}

func newOutputQueue() *outputQueue { return &outputQueue{wake: make(chan struct{})} }

func (q *outputQueue) push(stream Stream, data []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.items = append(q.items, Output{Stream: stream, Data: slices.Clone(data)})
	close(q.wake)
	q.wake = make(chan struct{})
}

func (q *outputQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.wake)
	}
}

func (q *outputQueue) next(ctx context.Context) (Output, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Output{}, err
		}
		q.mu.Lock()
		if q.head < len(q.items) {
			item := q.items[q.head]
			q.items[q.head] = Output{}
			q.head++
			if q.head == len(q.items) {
				q.items = nil
				q.head = 0
			}
			q.mu.Unlock()
			return item, nil
		}
		closed, wake := q.closed, q.wake
		q.mu.Unlock()
		if closed {
			return Output{}, io.EOF
		}
		select {
		case <-ctx.Done():
			return Output{}, ctx.Err()
		case <-wake:
		}
	}
}

type outputWriter struct {
	queue  *outputQueue
	stream Stream
}

func (w outputWriter) Write(data []byte) (int, error) {
	w.queue.push(w.stream, data)
	return len(data), nil
}
