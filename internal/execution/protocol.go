package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/internal/postgres"
	"github.com/ruohao1/circular/internal/runtimes"
)

const maxLineBytes = 1024 * 1024

func (s *Supervisor) ingest(ctx context.Context, id uuid.UUID, handle runtimes.Handle) error {
	output, err := s.docker.Output(ctx, handle)
	if err != nil {
		return executionFailure("could not read fake backend output", err)
	}
	buffers := map[runtimes.Stream][]byte{runtimes.Stdout: nil, runtimes.Stderr: nil}
	lines := map[runtimes.Stream]int{runtimes.Stdout: 0, runtimes.Stderr: 0}
	var failure error
	for chunk, err := range output {
		if err != nil {
			return executionFailure("could not read fake backend output", err)
		}
		if failure != nil {
			continue
		}
		data := chunk.Data
		for len(data) > 0 {
			end := bytes.IndexByte(data, '\n')
			if end < 0 {
				end = len(data)
			}
			if len(buffers[chunk.Stream])+end > maxLineBytes {
				failure = protocolFailure("fake backend JSON line exceeded the line limit", chunk.Stream, lines[chunk.Stream]+1, nil)
				break
			}
			buffers[chunk.Stream] = append(buffers[chunk.Stream], data[:end]...)
			if end == len(data) {
				break
			}
			lines[chunk.Stream]++
			kind, normalized, raw, err := decodeRecord(buffers[chunk.Stream], id, chunk.Stream, lines[chunk.Stream])
			buffers[chunk.Stream] = buffers[chunk.Stream][:0]
			if err != nil {
				failure = err
				break
			}
			if err := s.store.WithRun(ctx, id, func(r *postgres.RunResources) error { return r.AppendBackendEvent(kind, normalized, raw) }); err != nil {
				return executionFailure("could not persist "+kind+" for run "+id.String(), err)
			}
			data = data[end+1:]
		}
	}
	if failure == nil {
		for _, stream := range []runtimes.Stream{runtimes.Stdout, runtimes.Stderr} {
			if len(buffers[stream]) > 0 {
				failure = protocolFailure("fake backend ended with an incomplete JSON line", stream, lines[stream]+1, nil)
				break
			}
		}
	}
	result, err := s.docker.Wait(ctx, handle)
	if failure != nil {
		return failure
	}
	if err != nil {
		return executionFailure("could not determine fake backend completion", err)
	}
	if result.ExitCode == nil {
		return executionFailure("fake backend stopped before completing", nil)
	}
	if *result.ExitCode != 0 {
		return executionFailure(fmt.Sprintf("fake backend exited with code %d", *result.ExitCode), nil)
	}
	return nil
}

func protocolFailure(reason string, stream runtimes.Stream, line int, raw map[string]any) error {
	return &runFailure{message: fmt.Sprintf("%s at %s line %d", reason, stream, line), raw: raw}
}

func decodeRecord(line []byte, id uuid.UUID, stream runtimes.Stream, number int) (string, map[string]any, map[string]any, error) {
	record := "event"
	if stream == runtimes.Stderr {
		record = "error"
	}
	fail := func(reason string, raw map[string]any) (string, map[string]any, map[string]any, error) {
		return "", nil, nil, protocolFailure(reason, stream, number, raw)
	}
	if !utf8.Valid(line) {
		return fail("fake backend emitted an invalid UTF-8 JSON line", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	value, err := strictValue(decoder, line, 0)
	if errors.Is(err, errDuplicateField) {
		return fail("fake backend "+record+" contains a duplicate JSON field", nil)
	}
	if errors.Is(err, errNonfiniteJSON) {
		return fail("fake backend line is not valid JSON", nil)
	}
	if err != nil {
		return fail("fake backend emitted an invalid UTF-8 JSON line", nil)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fail("fake backend emitted an invalid UTF-8 JSON line", nil)
	}
	doc, ok := value.(map[string]any)
	if !ok {
		return fail("fake backend "+record+" must be a JSON object", nil)
	}
	if !validSurrogates(line) {
		return fail("fake backend "+record+" contains text that is not valid Unicode", nil)
	}
	if stream == runtimes.Stderr {
		if !fields(doc, "protocol_version", "error") && !fields(doc, "protocol_version", "run_id", "error") {
			return fail("fake backend error fields do not match protocol version 1", doc)
		}
		if doc["protocol_version"] != json.Number("1") {
			return fail("fake backend event uses an unsupported protocol version", doc)
		}
		problem, ok := doc["error"].(map[string]any)
		if !ok || !fields(problem, "code", "message") {
			return fail("fake backend error has invalid data", doc)
		}
		if _, ok := problem["message"].(string); !ok {
			return fail("fake backend error has unsupported data", doc)
		}
		switch problem["code"] {
		case "invalid_input":
			if !fields(doc, "protocol_version", "error") {
				return fail("fake backend invalid-input error fields do not match protocol version 1", doc)
			}
		case "injected_failure":
			if !fields(doc, "protocol_version", "run_id", "error") {
				return fail("fake backend injected-failure fields do not match protocol version 1", doc)
			}
			if doc["run_id"] != id.String() {
				return fail("fake backend event does not match the executing run", doc)
			}
		default:
			return fail("fake backend error has unsupported data", doc)
		}
		return "", nil, nil, &runFailure{message: "fake backend reported " + problem["code"].(string), raw: doc}
	}
	if !fields(doc, "protocol_version", "run_id", "source", "type", "data") {
		return fail("fake backend event fields do not match protocol version 1", doc)
	}
	if doc["protocol_version"] != json.Number("1") {
		return fail("fake backend event uses an unsupported protocol version", doc)
	}
	if doc["run_id"] != id.String() {
		return fail("fake backend event does not match the executing run", doc)
	}
	if doc["source"] != "fake-container-workload" {
		return fail("fake backend event has an unsupported source", doc)
	}
	kind, ok := doc["type"].(string)
	if !ok || kind != "agent.message.delta" && kind != "agent.message.completed" && kind != "usage.updated" {
		return fail("fake backend event has an unsupported type", doc)
	}
	data, ok := doc["data"].(map[string]any)
	if !ok {
		return fail("fake backend event data must be an object", doc)
	}
	switch kind {
	case "agent.message.delta":
		if _, ok := data["delta"].(string); !ok || !fields(data, "delta") {
			return fail("fake backend message delta has invalid data", doc)
		}
	case "agent.message.completed":
		if _, ok := data["content"].(string); !ok || !fields(data, "content") {
			return fail("fake backend completed message has invalid data", doc)
		}
	case "usage.updated":
		if !fields(data, "input_tokens", "output_tokens") || !nonnegativeInteger(data["input_tokens"]) || !nonnegativeInteger(data["output_tokens"]) {
			return fail("fake backend usage update has invalid data", doc)
		}
	}
	return kind, data, doc, nil
}

func fields(object map[string]any, names ...string) bool {
	if len(object) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}

func nonnegativeInteger(value any) bool {
	number, ok := value.(json.Number)
	if !ok || strings.ContainsAny(string(number), ".eE") {
		return false
	}
	return !strings.HasPrefix(string(number), "-") || number == "-0"
}

var (
	errDuplicateField = errors.New("duplicate JSON field")
	errNonfiniteJSON  = errors.New("non-finite JSON number")
	errInvalidJSON    = errors.New("invalid JSON value")
)

// Token decoding retains exact integer values and rejects duplicate fields at
// every depth. Standard Unmarshal would silently overwrite them. The recursion
// and integer digit limits match the Python protocol's bounded decoder behavior.
func strictValue(decoder *json.Decoder, line []byte, depth int) (any, error) {
	if depth > 1000 {
		return nil, errInvalidJSON
	}
	offset := decoder.InputOffset()
	token, err := decoder.Token()
	if err != nil {
		rest := bytes.TrimLeft(line[offset:], " \r\n\t:,")
		for _, constant := range []string{"NaN", "Infinity", "-Infinity"} {
			if bytes.HasPrefix(rest, []byte(constant)) {
				return nil, errNonfiniteJSON
			}
		}
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				name, ok := key.(string)
				if !ok {
					return nil, errInvalidJSON
				}
				child, err := strictValue(decoder, line, depth+1)
				if err != nil {
					return nil, err
				}
				if _, exists := object[name]; exists {
					return nil, errDuplicateField
				}
				object[name] = child
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errInvalidJSON
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				child, err := strictValue(decoder, line, depth+1)
				if err != nil {
					return nil, err
				}
				array = append(array, child)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errInvalidJSON
			}
			return array, nil
		default:
			return nil, errInvalidJSON
		}
	case json.Number:
		if strings.ContainsAny(string(value), ".eE") {
			number, err := strconv.ParseFloat(string(value), 64)
			if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
				return nil, errNonfiniteJSON
			}
		} else if len(strings.TrimPrefix(string(value), "-")) > 4300 {
			return nil, errInvalidJSON
		}
	}
	return token, nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Validate their
// original spelling after JSON parsing, before a lossy value can be persisted.
func validSurrogates(line []byte) bool {
	for i := 0; i < len(line); i++ {
		if line[i] != '"' {
			continue
		}
		for i++; i < len(line) && line[i] != '"'; i++ {
			if line[i] != '\\' {
				continue
			}
			i++
			if i >= len(line) {
				return false
			}
			if line[i] != 'u' {
				continue
			}
			if i+4 >= len(line) {
				return false
			}
			code, err := strconv.ParseUint(string(line[i+1:i+5]), 16, 16)
			if err != nil {
				return false
			}
			i += 4
			if code >= 0xdc00 && code <= 0xdfff {
				return false
			}
			if code >= 0xd800 && code <= 0xdbff {
				if i+6 >= len(line) || line[i+1] != '\\' || line[i+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(line[i+3:i+7]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	return true
}
