package backends

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/completion"
	"github.com/ruohao1/circular/internal/delegations"
	"github.com/ruohao1/circular/internal/prreviews"
	"github.com/ruohao1/circular/internal/runstate"
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
	if len(input.DelegationTargets) > 0 && (input.Kind == runstate.PRReview || delegations.ValidateTargets(input.DelegationTargets) != nil) {
		return Invocation{}, fakeFailure("invalid delegation targets", nil)
	}
	var config map[string]json.RawMessage
	_ = json.Unmarshal(input.Config, &config)
	if input.CompletionContract != nil {
		if input.Kind == runstate.PRReview || completion.ValidateContract(*input.CompletionContract) != nil {
			return Invocation{}, fakeFailure("invalid completion contract", nil)
		}
		request["completion_contract"] = input.CompletionContract
	}
	if raw, exists := config["completion_report"]; exists {
		var report completion.Report
		if input.CompletionContract == nil || prreviews.DecodeStrict(input.Config, &config) != nil || prreviews.DecodeStrict(raw, &report) != nil || completion.ValidateReport(input.CompletionContract.Policy, report) != nil {
			return Invocation{}, fakeFailure("invalid fake completion_report", nil)
		}
		request["completion_report"] = report
	}
	if raw, exists := config["delegation_request"]; exists {
		candidate, err := delegations.Decode(raw)
		if err != nil || prreviews.DecodeStrict(input.Config, &config) != nil {
			return Invocation{}, fakeFailure("invalid fake delegation_request", err)
		}
		if len(input.DelegationTargets) > 0 {
			if !delegations.Allows(input.DelegationTargets, candidate.AgentID) {
				return Invocation{}, fakeFailure("fake delegation target is not permitted", nil)
			}
			request["delegation_request"] = candidate
		}
	}
	if input.DelegationContextSHA256 != "" {
		if input.Kind == runstate.PRReview || len(input.DelegationTargets) > 0 || !prreviews.DigestPattern.MatchString(input.DelegationContextSHA256) {
			return Invocation{}, fakeFailure("invalid delegation continuation context", nil)
		}
		request["delegation_context_sha256"] = input.DelegationContextSHA256
	}
	stdin, err := json.Marshal(request)
	if err != nil {
		return Invocation{}, fakeFailure("could not encode fake workload input", err)
	}
	return Invocation{
		Command: []string{"--write-output"},
		Stdin:   append(stdin, '\n'),
		Decoder: &fakeDecoder{runID: input.RunID, handoff: codexDecoder{targets: input.DelegationTargets, completionContract: input.CompletionContract}},
	}, nil
}

type fakeDecoder struct {
	runID     uuid.UUID
	handoff   codexDecoder
	completed bool
}

func (d *fakeDecoder) Decode(line []byte, stream runtimes.Stream, number int) ([]Event, error) {
	var header struct{ Type string }
	if json.Unmarshal(line, &header) == nil && (header.Type == "circular.delegation.requested" || header.Type == "circular.task.completion.reported") {
		return d.handoff.Decode(line, stream, number)
	}
	kind, data, raw, err := decodeFakeRecord(line, d.runID, stream, number)
	if err != nil {
		return nil, err
	}
	if kind == "agent.message.completed" {
		d.completed = true
	}
	return []Event{{Type: kind, Source: "fake-container-workload", Data: data, Raw: raw}}, nil
}

func (d *fakeDecoder) Finish() error {
	if d.handoff.delegation != nil && !d.completed {
		return fakeFailure("fake delegation ended without a completed message", nil)
	}
	return nil
}

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
