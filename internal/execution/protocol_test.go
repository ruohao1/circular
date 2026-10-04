package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/backends"
	"github.com/ruohao1/circular/internal/runtimes"
)

func protocolTestBackend(t *testing.T, id uuid.UUID, name string) preparedBackend {
	t.Helper()
	var adapter backends.Backend = backends.Fake{}
	if name == "codex" {
		adapter = backends.Codex{}
	}
	invocation, err := adapter.Prepare(backends.Input{RunID: id, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return preparedBackend{name: name, invocation: invocation}
}

func protocolTestOutput(chunks ...runtimes.Output) iter.Seq2[runtimes.Output, error] {
	return func(yield func(runtimes.Output, error) bool) {
		for _, chunk := range chunks {
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

func TestIngestOutputRetainsProgressAcrossStderrFailure(t *testing.T) {
	id := uuid.New()
	delta := []byte(fmt.Sprintf(`{"protocol_version":1,"run_id":%q,"source":"fake-container-workload","type":"agent.message.delta","data":{"delta":"first"}}`+"\n", id.String()))
	diagnostic := []byte(fmt.Sprintf(`{"protocol_version":1,"run_id":%q,"error":{"code":"injected_failure","message":"after first event"}}`+"\n", id.String()))
	for _, test := range []struct {
		name   string
		chunks []runtimes.Output
	}{
		{"stdout_first", []runtimes.Output{{Stream: runtimes.Stdout, Data: delta}, {Stream: runtimes.Stderr, Data: diagnostic}}},
		{"stderr_first", []runtimes.Output{{Stream: runtimes.Stderr, Data: diagnostic}, {Stream: runtimes.Stdout, Data: delta}}},
		{"stderr_between_stdout_fragments", []runtimes.Output{{Stream: runtimes.Stdout, Data: delta[:20]}, {Stream: runtimes.Stderr, Data: diagnostic}, {Stream: runtimes.Stdout, Data: delta[20:]}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := 0
			err, drained := ingestOutput(id, protocolTestBackend(t, id, "fake"), protocolTestOutput(test.chunks...), func(event backends.Event) error {
				events++
				if event.Source != "fake-container-workload" || event.Type != "agent.message.delta" || event.Data["delta"] != "first" || event.Raw["run_id"] != id.String() {
					t.Fatalf("unexpected event: %#v", event)
				}
				return nil
			})
			if !drained {
				t.Fatal("output was not drained")
			}
			message, raw := failureProjection(err)
			problem, ok := raw["error"].(map[string]any)
			if message != "fake backend reported injected_failure" || raw["run_id"] != id.String() || !ok || problem["code"] != "injected_failure" || problem["message"] != "after first event" {
				t.Fatalf("lost diagnostic: %s %v", message, raw)
			}
			if events != 1 {
				t.Fatalf("retained %d progress events, want 1", events)
			}
		})
	}
}

func TestIngestOutputStopsInterpretingAfterProtocolViolation(t *testing.T) {
	id := uuid.New()
	delta := []byte(fmt.Sprintf(`{"protocol_version":1,"run_id":%q,"source":"fake-container-workload","type":"agent.message.delta","data":{"delta":"must not persist"}}`+"\n", id.String()))
	for _, stream := range []runtimes.Stream{runtimes.Stdout, runtimes.Stderr} {
		t.Run(string(stream), func(t *testing.T) {
			output := protocolTestOutput(runtimes.Output{Stream: stream, Data: []byte("not-json\n")}, runtimes.Output{Stream: runtimes.Stdout, Data: delta})
			err, drained := ingestOutput(id, protocolTestBackend(t, id, "fake"), output, func(backends.Event) error {
				t.Fatal("persisted an event after a protocol violation")
				return nil
			})
			message, _ := failureProjection(err)
			if !drained || !strings.Contains(message, "invalid UTF-8 JSON line at "+string(stream)+" line 1") {
				t.Fatalf("drained=%v failure=%s", drained, message)
			}
		})
	}
}

func TestIngestOutputPreservesCodexTermination(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failure_%t", fail), func(t *testing.T) {
			id := uuid.New()
			backend := protocolTestBackend(t, id, "codex")
			chunks := []runtimes.Output{{Stream: runtimes.Stderr, Data: []byte(strings.Repeat("private diagnostic", maxLineBytes/10))}}
			if fail {
				chunks = append(chunks, runtimes.Output{Stream: runtimes.Stdout, Data: []byte("{\"type\":\"turn.failed\",\"error\":{\"message\":\"failed\"}}\n")})
			}
			chunks = append(chunks, runtimes.Output{Stream: runtimes.Stdout, Data: []byte("{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"result\"}}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}\n")})
			var events []backends.Event
			err, drained := ingestOutput(id, backend, protocolTestOutput(chunks...), func(event backends.Event) error {
				events = append(events, event)
				return nil
			})
			if !drained {
				t.Fatal("output was not drained")
			}
			if fail {
				message, raw := failureProjection(err)
				if len(events) != 0 || message != "Codex backend reported turn.failed" || raw["type"] != "turn.failed" {
					t.Fatalf("Codex failure changed: %v %s %v", events, message, raw)
				}
			} else if err != nil || backend.invocation.Decoder.Finish() != nil || len(events) != 2 || events[0].Data["content"] != "result" || events[1].Type != "usage.updated" {
				t.Fatalf("Codex completion changed: %v %v", events, err)
			}
		})
	}
}

func TestIngestOutputAbortsOnTransportAndPersistenceErrors(t *testing.T) {
	for _, transport := range []bool{false, true} {
		t.Run(fmt.Sprintf("transport_%t", transport), func(t *testing.T) {
			id := uuid.New()
			cause := errors.New("injected boundary failure")
			chunk := runtimes.Output{Stream: runtimes.Stdout, Data: []byte(fmt.Sprintf(`{"protocol_version":1,"run_id":%q,"source":"fake-container-workload","type":"agent.message.delta","data":{"delta":"first"}}`+"\n", id.String()))}
			output := iter.Seq2[runtimes.Output, error](func(yield func(runtimes.Output, error) bool) {
				var readErr error
				if transport {
					readErr = cause
				}
				if yield(chunk, readErr) {
					t.Fatal("continued after an immediate failure")
				}
			})
			err, drained := ingestOutput(id, protocolTestBackend(t, id, "fake"), output, func(backends.Event) error {
				if transport {
					t.Fatal("persisted output after a transport error")
				}
				return cause
			})
			if drained || !errors.Is(err, cause) {
				t.Fatalf("drained=%v error=%v", drained, err)
			}
		})
	}
}

func TestIngestOutputFailurePrecedenceAfterDiagnostic(t *testing.T) {
	for _, mode := range []string{"transport", "cancellation", "persistence", "incomplete"} {
		t.Run(mode, func(t *testing.T) {
			id := uuid.New()
			cause := errors.New("injected boundary failure")
			if mode == "cancellation" {
				cause = context.Canceled
			}
			diagnostic := runtimes.Output{Stream: runtimes.Stderr, Data: []byte(fmt.Sprintf(`{"protocol_version":1,"run_id":%q,"error":{"code":"injected_failure","message":"after first event"}}`+"\n", id.String()))}
			delta := runtimes.Output{Stream: runtimes.Stdout, Data: []byte(fmt.Sprintf(`{"protocol_version":1,"run_id":%q,"source":"fake-container-workload","type":"agent.message.delta","data":{"delta":"first"}}`+"\n", id.String()))}
			output := iter.Seq2[runtimes.Output, error](func(yield func(runtimes.Output, error) bool) {
				if !yield(diagnostic, nil) {
					t.Fatal("stopped draining after a diagnostic")
				}
				var readErr error
				if mode == "transport" || mode == "cancellation" {
					readErr = cause
				} else if mode == "incomplete" {
					delta.Data = delta.Data[:20]
				}
				if keepReading := yield(delta, readErr); keepReading != (mode == "incomplete") {
					t.Fatalf("continued=%v after %s", keepReading, mode)
				}
			})
			err, drained := ingestOutput(id, protocolTestBackend(t, id, "fake"), output, func(backends.Event) error {
				if mode != "persistence" {
					t.Fatal("persisted invalid output")
				}
				return cause
			})
			if mode == "incomplete" {
				message, raw := failureProjection(err)
				if !drained || message != "fake backend reported injected_failure" || raw["run_id"] != id.String() {
					t.Fatalf("lost diagnostic: drained=%v failure=%s raw=%v", drained, message, raw)
				}
			} else if drained || !errors.Is(err, cause) {
				t.Fatalf("failure did not override diagnostic: drained=%v error=%v", drained, err)
			}
		})
	}
}
