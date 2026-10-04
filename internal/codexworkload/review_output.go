package codexworkload

import (
	"bytes"
	"io"
	"sync"
)

// CLI writes can end halfway through a JSON record. The separate tool channel
// must never insert a report into that partial record or race token redaction.
type reviewOutput struct {
	mu     sync.Mutex
	writer io.Writer
	buffer []byte
}

func (w *reviewOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			if len(w.buffer)+len(data) > maxOutputLine {
				return 0, errPrivateOutput
			}
			w.buffer = append(w.buffer, data...)
			break
		}
		if len(w.buffer)+end > maxOutputLine {
			return 0, errPrivateOutput
		}
		w.buffer = append(w.buffer, data[:end+1]...)
		if _, err := w.writer.Write(w.buffer); err != nil {
			return 0, err
		}
		w.buffer = w.buffer[:0]
		data = data[end+1:]
	}
	return total, nil
}
func (w *reviewOutput) record(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}
func (w *reviewOutput) finish() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buffer) != 0 {
		return errPrivateOutput
	}
	return nil
}

type recordWriter struct{ output *reviewOutput }

func (w recordWriter) Write(data []byte) (int, error) { return w.output.record(data) }
