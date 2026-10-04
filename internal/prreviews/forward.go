package prreviews

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync"
	"time"
)

var ErrForwardReport = errors.New("review report could not be forwarded to the worker")

// ReportForwarder sends a validated canonical record to the workload's stdout
// before the private tool acknowledges it. Docker retains that stream even if
// the model hangs or its container exits; worker success is a separate gate.
type ReportForwarder struct {
	listener  net.Listener
	spool     *Spool
	writer    io.Writer
	done      chan struct{}
	once      sync.Once
	delivered string
	finishErr error
}

func StartReportForwarder(directory string, spool *Spool, writer io.Writer) (*ReportForwarder, error) {
	listener, err := net.Listen("unix", filepath.Join(directory, "report.sock"))
	if err != nil {
		return nil, err
	}
	f := &ReportForwarder{listener: listener, spool: spool, writer: writer, done: make(chan struct{})}
	go f.serve()
	return f, nil
}
func (f *ReportForwarder) serve() {
	defer close(f.done)
	for {
		connection, err := f.listener.Accept()
		if err != nil {
			return
		}
		_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
		line, err := bufio.NewReader(io.LimitReader(connection, 65)).ReadString('\n')
		if err == nil && len(line) == 65 {
			checksum := line[:64]
			if _, err = hex.DecodeString(checksum); err == nil {
				if f.delivered == "" {
					err = f.spool.publish(f.writer, checksum)
					if err == nil {
						f.delivered = checksum
					}
				} else if f.delivered != checksum {
					err = ErrConflictingReport
				}
				if err == nil {
					_, _ = io.WriteString(connection, line)
				}
			}
		}
		_ = connection.Close()
	}
}

// Finish closes the acknowledgement channel and drains any valid report whose
// submission was interrupted before acknowledgement. Identical retries emit once.
func (f *ReportForwarder) Finish() error {
	f.once.Do(func() {
		_ = f.listener.Close()
		<-f.done
		if f.delivered == "" {
			f.finishErr = f.spool.Publish(f.writer)
		}
	})
	return f.finishErr
}
func ForwardReport(ctx context.Context, directory, checksum string) error {
	if len(checksum) != 64 {
		return ErrForwardReport
	}
	connection, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", filepath.Join(directory, "report.sock"))
	if err != nil {
		return ErrForwardReport
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = io.WriteString(connection, checksum+"\n"); err != nil {
		return ErrForwardReport
	}
	reply, err := bufio.NewReader(io.LimitReader(connection, 65)).ReadString('\n')
	if err != nil || reply != checksum+"\n" {
		return ErrForwardReport
	}
	return nil
}
