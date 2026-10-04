package backends

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/runtimes"
)

// Fake implements the deterministic version 1 workload used in local development
// and lifecycle integration tests.
type Fake struct{ DelayMS int }

func (f Fake) Prepare(input Input) (Invocation, error) {
	behavior, err := f.behavior(input.Config)
	if err != nil {
		return Invocation{}, err
	}
	request := map[string]any{
		"protocol_version": 1,
		"run": map[string]any{
			"id":               input.RunID.String(),
			"task_title":       input.TaskTitle,
			"task_description": input.TaskDescription,
			"instructions":     input.Instructions,
		},
		"behavior": behavior,
	}
	stdin, err := json.Marshal(request)
	if err != nil {
		return Invocation{}, fakeFailure("could not encode fake workload input", err)
	}
	return Invocation{
		Command: []string{"--write-output"},
		Stdin:   append(stdin, '\n'),
		Decoder: &fakeDecoder{runID: input.RunID},
	}, nil
}

type fakeDecoder struct{ runID uuid.UUID }

func (d *fakeDecoder) Decode(line []byte, stream runtimes.Stream, number int) ([]Event, error) {
	kind, data, raw, err := decodeFakeRecord(line, d.runID, stream, number)
	if err != nil {
		return nil, err
	}
	return []Event{{Type: kind, Source: "fake-container-workload", Data: data, Raw: raw}}, nil
}

func (d *fakeDecoder) Finish() error { return nil }

func fakeFailure(message string, cause error) error {
	return &Failure{Message: message, Cause: cause}
}

func (f Fake) behavior(raw json.RawMessage) (map[string]any, error) {
	var config map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		return nil, fakeFailure("invalid fake backend configuration", err)
	}
	delay := f.DelayMS
	if value, ok := config["delay_ms"]; ok {
		if !nonnegativeInteger(value) {
			return nil, fakeFailure("fake delay_ms must be an integer from 0 through 10000", nil)
		}
		var err error
		delay, err = strconv.Atoi(string(value.(json.Number)))
		if err != nil || delay < 0 || delay > 10000 {
			return nil, fakeFailure("fake delay_ms must be an integer from 0 through 10000", nil)
		}
	}
	failure := "none"
	if value, ok := config["failure"]; ok {
		failure, _ = value.(string)
		if failure != "none" && failure != "before_events" && failure != "after_first_event" {
			return nil, fakeFailure("unsupported fake failure mode", nil)
		}
	}
	return map[string]any{"delay_ms": delay, "failure": failure}, nil
}
