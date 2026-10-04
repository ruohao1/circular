package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runtimes"
)

const maxLineBytes = backends.MaxLineBytes

func (s *Supervisor) ingest(ctx context.Context, id uuid.UUID, handle runtimes.Handle, backend preparedBackend) error {
	output, err := s.docker.Output(ctx, handle)
	if err != nil {
		return executionFailure("could not read "+backend.name+" backend output", err)
	}
	failure, drained := ingestOutput(id, backend, output, func(event backends.Event) error {
		return s.store.WithRun(ctx, id, func(r *postgres.RunResources) error {
			return r.AppendBackendEvent(event.Source, event.Type, event.Data, event.Raw)
		})
	})
	if !drained {
		return failure
	}
	result, err := s.docker.Wait(ctx, handle)
	if failure != nil {
		return failure
	}
	if err != nil {
		return executionFailure("could not determine "+backend.name+" backend completion", err)
	}
	if result.ExitCode == nil {
		return executionFailure(backend.name+" backend stopped before completing", nil)
	}
	if *result.ExitCode != 0 {
		return executionFailure(fmt.Sprintf("%s backend exited with code %d", backend.name, *result.ExitCode), nil)
	}
	return backend.invocation.Decoder.Finish()
}

// ingestOutput frames and decodes output, stopping immediately on transport or
// persistence errors. drained reports whether runtime completion can be awaited.
func ingestOutput(id uuid.UUID, backend preparedBackend, output iter.Seq2[runtimes.Output, error], appendEvent func(backends.Event) error) (error, bool) {
	buffers := map[runtimes.Stream][]byte{runtimes.Stdout: nil, runtimes.Stderr: nil}
	lines := map[runtimes.Stream]int{runtimes.Stdout: 0, runtimes.Stderr: 0}
	var failure error
	var reported *backends.ReportedFailure
	for chunk, err := range output {
		if err != nil {
			return executionFailure("could not read "+backend.name+" backend output", err), false
		}
		if backend.invocation.StderrText && chunk.Stream == runtimes.Stderr {
			continue
		}
		if failure != nil || reported != nil && chunk.Stream == runtimes.Stderr {
			continue
		}
		data := chunk.Data
		for len(data) > 0 {
			end := bytes.IndexByte(data, '\n')
			if end < 0 {
				end = len(data)
			}
			if len(buffers[chunk.Stream])+end > maxLineBytes {
				failure = protocolFailure(backend.name+" backend JSON line exceeded the line limit", chunk.Stream, lines[chunk.Stream]+1, nil)
				break
			}
			buffers[chunk.Stream] = append(buffers[chunk.Stream], data[:end]...)
			if end == len(data) {
				break
			}
			lines[chunk.Stream]++
			events, err := backend.invocation.Decoder.Decode(buffers[chunk.Stream], chunk.Stream, lines[chunk.Stream])
			buffers[chunk.Stream] = buffers[chunk.Stream][:0]
			if err != nil {
				// The streams are observed independently. A valid stderr failure
				// must not discard earlier progress still buffered on stdout.
				var diagnostic *backends.ReportedFailure
				if errors.As(err, &diagnostic) {
					reported = diagnostic
					break
				}
				failure = err
				break
			}
			for _, event := range events {
				if err := appendEvent(event); err != nil {
					return executionFailure("could not persist "+event.Type+" for run "+id.String(), err), false
				}
			}
			data = data[end+1:]
		}
	}
	if failure == nil {
		for _, stream := range []runtimes.Stream{runtimes.Stdout, runtimes.Stderr} {
			if len(buffers[stream]) > 0 {
				failure = protocolFailure(backend.name+" backend ended with an incomplete JSON line", stream, lines[stream]+1, nil)
				break
			}
		}
	}
	if reported != nil {
		return reported, true
	}
	return failure, true
}

func protocolFailure(reason string, stream runtimes.Stream, line int, raw map[string]any) error {
	return &runFailure{message: fmt.Sprintf("%s at %s line %d", reason, stream, line), raw: raw}
}
