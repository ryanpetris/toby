package diagnostic

// Non-failing diagnostic output and foreground suppression.

import (
	"io"
	"sync"
	"sync/atomic"
)

type sink struct {
	mu sync.Mutex

	output     io.Writer
	suppressed atomic.Bool
}

var _ io.Writer = (*sink)(nil)

func newSink(output io.Writer) *sink {
	if output == nil {
		output = io.Discard
	}

	return &sink{output: output}
}

func (s *sink) Write(data []byte) (int, error) {
	if s == nil || s.suppressed.Load() {
		return len(data), nil
	}

	s.mu.Lock()
	if !s.suppressed.Load() {
		written, err := s.output.Write(data)
		if err == nil && written != len(data) {
			err = io.ErrShortWrite
		}
		DiscardError(
			"the diagnostic sink cannot report its own output failure",
			"write diagnostic output",
			err,
			"bytes", len(data),
			"bytes_written", written,
		)
	}
	s.mu.Unlock()

	return len(data), nil
}

func (s *sink) suppress() {
	if s == nil {
		return
	}

	s.suppressed.Store(true)
	s.mu.Lock()
	s.output = io.Discard
	s.mu.Unlock()
}
