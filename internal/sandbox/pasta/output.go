package pasta

// Bounded capture for Pasta startup and terminal diagnostics.

import (
	"bytes"
	"sync"
)

const maxDiagnosticBytes = 64 << 10

type diagnosticOutput struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	truncated bool
}

func (o *diagnosticOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	remaining := maxDiagnosticBytes - o.buffer.Len()
	if remaining > 0 {
		_, _ = o.buffer.Write(value[:min(len(value), remaining)])
	}
	if len(value) > remaining {
		o.truncated = true
	}

	return len(value), nil
}

func (o *diagnosticOutput) String() string {
	if o == nil {
		return ""
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	result := o.buffer.String()
	if o.truncated {
		result += "\n[Pasta diagnostic output truncated]"
	}
	return result
}
