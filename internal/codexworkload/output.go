package codexworkload

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ruohao1/circular/internal/codexauth"
)

const maxOutputLine = 1024 * 1024

var errPrivateOutput = errors.New("Codex subscription output could not be safely forwarded")

// subscriptionOutput sees complete records before they reach Docker stdout.
// Refresh tokens are read from the persistent login before each record, and all
// previous values remain redacted for the rest of this execution.
type subscriptionOutput struct {
	writer  io.Writer
	authDir string
	secrets []string
	buffer  []byte
	cancel  context.CancelFunc
	failed  bool
	context context.Context
}

func (w *subscriptionOutput) reject() error {
	if !w.failed {
		w.failed = true
		w.buffer = nil
		publicError(w.writer, "Codex subscription output is unavailable; check the saved ChatGPT login and retry")
		w.cancel()
	}
	return errPrivateOutput
}

func (w *subscriptionOutput) rejectAuth() error {
	if !w.failed {
		w.failed = true
		w.buffer = nil
		publicAuthError(w.writer)
		w.cancel()
	}
	return errPrivateOutput
}

func (w *subscriptionOutput) Write(data []byte) (int, error) {
	if w.failed {
		return 0, errPrivateOutput
	}
	total := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			end = len(data)
		}
		if len(w.buffer)+end > maxOutputLine {
			return 0, w.reject()
		}
		w.buffer = append(w.buffer, data[:end]...)
		if end == len(data) {
			break
		}
		if err := w.record(); err != nil {
			if errors.Is(err, codexauth.ErrLogin) || errors.Is(err, codexauth.ErrDirectory) {
				return 0, w.rejectAuth()
			}
			return 0, w.reject()
		}
		w.buffer = w.buffer[:0]
		data = data[end+1:]
	}
	return total, nil
}

func (w *subscriptionOutput) record() error {
	secrets, err := w.currentSecrets()
	if err != nil {
		return err
	}
	for _, secret := range secrets {
		found := false
		for _, previous := range w.secrets {
			found = found || previous == secret
		}
		if !found {
			w.secrets = append(w.secrets, secret)
		}
	}
	sort.Slice(w.secrets, func(i, j int) bool { return len(w.secrets[i]) > len(w.secrets[j]) })
	if !utf8.Valid(w.buffer) || !validSurrogates(w.buffer) {
		return errPrivateOutput
	}
	decoder := json.NewDecoder(bytes.NewReader(w.buffer))
	decoder.UseNumber()
	value, err := privateJSON(decoder, 0)
	if err != nil || decoder.Decode(new(any)) != io.EOF {
		return errPrivateOutput
	}
	if _, ok := value.(map[string]any); !ok {
		return errPrivateOutput
	}
	return json.NewEncoder(w.writer).Encode(redact(value, w.secrets))
}

// The CLI may replace auth.json while the stdout pipe is being drained. Keep
// output buffered through a short in-progress write; persistent invalid state
// still fails closed before any record is published.
func (w *subscriptionOutput) currentSecrets() ([]string, error) {
	ctx := w.context
	if ctx == nil {
		ctx = context.Background()
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		secrets, err := codexauth.Secrets(w.authDir)
		if err == nil || !errors.Is(err, codexauth.ErrLogin) || !time.Now().Before(deadline) {
			return secrets, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (w *subscriptionOutput) finish() error {
	if w.failed {
		return errPrivateOutput
	}
	if codexauth.ValidateChatGPT(w.authDir) != nil {
		return w.rejectAuth()
	}
	if len(w.buffer) != 0 {
		return w.reject()
	}
	return nil
}

func redact(value any, secrets []string) any {
	switch value := value.(type) {
	case string:
		for _, secret := range secrets {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
		return value
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			result[redact(key, secrets).(string)] = redact(child, secrets)
		}
		return result
	case []any:
		for i, child := range value {
			value[i] = redact(child, secrets)
		}
	}
	return value
}

// Detect duplicate keys before a lossy Unmarshal can discard part of a record.
func privateJSON(decoder *json.Decoder, depth int) (any, error) {
	if depth > 1000 {
		return nil, errPrivateOutput
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if token == json.Delim('{') {
		result := map[string]any{}
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return nil, errPrivateOutput
			}
			if _, duplicate := result[name]; duplicate {
				return nil, errPrivateOutput
			}
			child, err := privateJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = child
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errPrivateOutput
		}
		return result, nil
	}
	if token == json.Delim('[') {
		result := []any{}
		for decoder.More() {
			child, err := privateJSON(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, child)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errPrivateOutput
		}
		return result, nil
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
