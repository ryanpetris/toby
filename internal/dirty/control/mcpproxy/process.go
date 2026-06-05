package mcpproxy

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ProcessHandle abstracts a running MCP sidecar — either a local child process
// (startProcess, used by tests) or a container (DockerRunner). The lifecycle and
// stdio bridge only depend on Stdin/Stdout/Wait/Stop/PID.
type ProcessHandle struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	wait   chan ProcessResult
	stop   func(context.Context) error
	pid    int
	once   sync.Once
}

func startProcess(ctx context.Context, argv []string, env map[string]string, stdio bool, externalStop func(context.Context) error) (*ProcessHandle, error) {
	if len(argv) == 0 {
		return nil, errors.New("missing command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = envList(env)
	cmd.Stderr = io.Discard
	handle := &ProcessHandle{wait: make(chan ProcessResult, 1)}
	if stdio {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		handle.stdin = stdin
		handle.stdout = stdout
	} else {
		cmd.Stdout = io.Discard
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if cmd.Process != nil {
		handle.pid = cmd.Process.Pid
	}
	handle.stop = func(ctx context.Context) error {
		var err error
		if externalStop != nil {
			err = externalStop(ctx)
		}
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-handle.wait:
		case <-time.After(2 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-ctx.Done():
			err = ctx.Err()
		}
		return err
	}
	go func() {
		handle.wait <- processResult(cmd.Wait())
		close(handle.wait)
	}()
	return handle, nil
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

func processResult(err error) ProcessResult {
	if err == nil {
		return ProcessResult{ExitCode: 0}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return ProcessResult{ExitCode: exitErr.ExitCode(), Err: err}
	}
	if errors.Is(err, context.Canceled) {
		return ProcessResult{ExitCode: 130, Err: err}
	}
	return ProcessResult{ExitCode: 1, Err: err}
}

func envList(env map[string]string) []string {
	if env == nil {
		return os.Environ()
	}
	values := os.Environ()
	for name, value := range env {
		values = append(values, name+"="+value)
	}
	return values
}
