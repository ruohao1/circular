// Package fakeworkload is the deterministic, isolated version-1 backend fixture.
package fakeworkload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/google/uuid"
)

type request struct {
	id, title, description, instructions, failure string
	delay                                         time.Duration
}

// Run accepts only the versioned request on stdin. Output and exit codes form
// the backend interface; trusted callers opt into writing the Run-scoped file.
func Run(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, writeOutput bool) int {
	if ctx.Err() != nil {
		return 1
	}
	input, err := io.ReadAll(io.LimitReader(stdin, 16*1024*1024+1))
	var value request
	if err == nil && len(input) <= 16*1024*1024 {
		value, err = parse(input)
	} else {
		err = errors.New("stdin must contain one bounded JSON object")
	}
	if err != nil {
		_ = line(stderr, map[string]any{"protocol_version": 1, "error": map[string]string{"code": "invalid_input", "message": err.Error()}})
		return 2
	}
	failure := func(message string) int {
		_ = line(stderr, map[string]any{"protocol_version": 1, "run_id": value.id, "error": map[string]string{"code": "injected_failure", "message": message}})
		return 20
	}
	if value.failure == "before_events" {
		return failure("injected failure before emitting events")
	}
	content := "Fake container workload completed: " + value.title
	if writeOutput {
		file, err := os.OpenFile("circular-result-"+value.id+".txt", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return 1
		}
		_, err = io.WriteString(file, content+"\n")
		closed := file.Close()
		if err != nil || closed != nil {
			return 1
		}
	}
	words := func(text string) int {
		return len(strings.FieldsFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }))
	}
	events := []struct {
		kind string
		data map[string]any
	}{
		{"agent.message.delta", map[string]any{"delta": "Fake container workload completed: "}},
		{"agent.message.delta", map[string]any{"delta": value.title}},
		{"agent.message.completed", map[string]any{"content": content}},
		{"usage.updated", map[string]any{"input_tokens": words(value.title) + words(value.description) + words(value.instructions), "output_tokens": words(content)}},
	}
	for i, event := range events {
		timer := time.NewTimer(value.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 1
		case <-timer.C:
		}
		if line(stdout, map[string]any{"protocol_version": 1, "run_id": value.id, "source": "fake-container-workload", "type": event.kind, "data": event.data}) != nil {
			return 1
		}
		if i == 0 && value.failure == "after_first_event" {
			return failure("injected failure after first event")
		}
	}
	return 0
}

func line(out io.Writer, value any) error {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	var ascii strings.Builder
	for _, r := range buffer.String() {
		if r < 127 {
			ascii.WriteRune(r)
		} else if r <= 0xffff {
			fmt.Fprintf(&ascii, "\\u%04x", r)
		} else {
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(&ascii, "\\u%04x\\u%04x", hi, lo)
		}
	}
	_, err := io.WriteString(out, ascii.String())
	return err
}

func parse(data []byte) (request, error) {
	bad := func(message string) (request, error) { return request{}, errors.New(message) }
	if !utf8.Valid(data) {
		return bad("stdin must be valid UTF-8 JSON")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := duplicates(d, 0); err != nil {
		var duplicate *duplicateField
		if errors.As(err, &duplicate) {
			return bad("input contains duplicate field: " + duplicate.name)
		}
		return bad("stdin must contain one JSON object")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return bad("stdin must contain one JSON object")
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(data, &input) != nil || input == nil {
		return bad("stdin must contain one JSON object")
	}
	if err := fields(input, "input", "protocol_version", "run", "behavior"); err != nil {
		return request{}, err
	}
	if string(bytes.TrimSpace(input["protocol_version"])) != "1" {
		return bad("unsupported protocol_version: expected 1")
	}
	var run, behavior map[string]json.RawMessage
	if json.Unmarshal(input["run"], &run) != nil || run == nil {
		return bad("run must be an object")
	}
	if err := fields(run, "run", "id", "task_title", "task_description", "instructions"); err != nil {
		return request{}, err
	}
	var value request
	if json.Unmarshal(run["id"], &value.id) != nil {
		return bad("run.id must be a canonical UUID")
	}
	id, err := uuid.Parse(value.id)
	if err != nil || id.String() != value.id {
		return bad("run.id must be a canonical UUID")
	}
	for _, field := range []struct {
		name   string
		target *string
	}{{"task_title", &value.title}, {"task_description", &value.description}, {"instructions", &value.instructions}} {
		var text *string
		if json.Unmarshal(run[field.name], &text) != nil || text == nil {
			if field.name == "task_title" {
				return bad("run.task_title must be a non-empty string")
			}
			return bad("run." + field.name + " must be a string")
		}
		if !validUnicode(run[field.name]) {
			return bad("run." + field.name + " must be valid Unicode text")
		}
		*field.target = *text
	}
	if strings.TrimFunc(value.title, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }) == "" {
		return bad("run.task_title must be a non-empty string")
	}
	if json.Unmarshal(input["behavior"], &behavior) != nil || behavior == nil {
		return bad("behavior must be an object")
	}
	if err := fields(behavior, "behavior", "delay_ms", "failure"); err != nil {
		return request{}, err
	}
	delay, err := strconv.Atoi(string(bytes.TrimSpace(behavior["delay_ms"])))
	if err != nil || delay < 0 || delay > 10000 {
		return bad("behavior.delay_ms must be an integer from 0 through 10000")
	}
	value.delay = time.Duration(delay) * time.Millisecond
	if json.Unmarshal(behavior["failure"], &value.failure) != nil || value.failure != "none" && value.failure != "before_events" && value.failure != "after_first_event" {
		return bad("behavior.failure must be one of: none, before_events, after_first_event")
	}
	return value, nil
}

func fields(value map[string]json.RawMessage, scope string, names ...string) error {
	required := map[string]bool{}
	for _, name := range names {
		required[name] = true
	}
	unknown, missing := []string{}, []string{}
	for name := range value {
		if !required[name] {
			unknown = append(unknown, name)
		}
	}
	for name := range required {
		if _, ok := value[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(unknown)
	sort.Strings(missing)
	if len(unknown) > 0 {
		return errors.New(scope + " contains unsupported fields: " + strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		return errors.New(scope + " is missing required fields: " + strings.Join(missing, ", "))
	}
	return nil
}

type duplicateField struct{ name string }

func (e *duplicateField) Error() string { return "duplicate field" }
func duplicates(d *json.Decoder, depth int) error {
	if depth > 1000 {
		return errors.New("input too deep")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	if token == json.Delim('{') {
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("invalid key")
			}
			if err := duplicates(d, depth+1); err != nil {
				return err
			}
			if seen[name] {
				return &duplicateField{name}
			}
			seen[name] = true
		}
		_, err = d.Token()
		return err
	}
	if token == json.Delim('[') {
		for d.More() {
			if err := duplicates(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

// Validate original UTF-16 escape spelling before encoding/json can replace it.
func validUnicode(raw []byte) bool {
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return false
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
