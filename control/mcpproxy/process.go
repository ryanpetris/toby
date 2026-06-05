package mcpproxy

// ProcessHandle: the transport-agnostic handle the lifecycle and stdio bridge
// depend on (stdin/stdout/wait/stop/pid). The container-backed handle is built
// by the DockerRunner.

import (
	"context"
	"io"
	"sync"
)

// ProcessHandle abstracts a running MCP sidecar (a container managed by the
// DockerRunner). The lifecycle and stdio bridge only depend on
// Stdin/Stdout/Wait/Stop/PID.
type ProcessHandle struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	wait   chan ProcessResult
	stop   func(context.Context) error
	pid    int
	once   sync.Once
}

func (h *ProcessHandle) Stop(ctx context.Context) error {
	if h == nil {
		return nil
	}
	var err error
	h.once.Do(func() {
		if h.stop != nil {
			err = h.stop(ctx)
		}
	})
	return err
}

func (h *ProcessHandle) Wait() <-chan ProcessResult {
	if h == nil {
		ch := make(chan ProcessResult)
		close(ch)
		return ch
	}
	return h.wait
}

func (h *ProcessHandle) Stdin() io.WriteCloser {
	if h == nil {
		return nil
	}
	return h.stdin
}

func (h *ProcessHandle) Stdout() io.ReadCloser {
	if h == nil {
		return nil
	}
	return h.stdout
}

func (h *ProcessHandle) PID() int {
	if h == nil {
		return 0
	}
	return h.pid
}
