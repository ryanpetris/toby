package mcpproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type ProcessHandle struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	wait   chan ProcessResult
	stop   func(context.Context) error
	once   sync.Once
}

func startProcess(ctx context.Context, argv []string, env map[string]string, stdio bool, stop func(context.Context) error) (*ProcessHandle, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("missing command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = envList(env)
	cmd.Stderr = io.Discard
	handle := &ProcessHandle{cmd: cmd, wait: make(chan ProcessResult, 1), stop: stop}
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
		if h.cmd != nil && h.cmd.Process != nil {
			_ = h.cmd.Process.Signal(syscall.SIGTERM)
		}
		select {
		case <-h.wait:
		case <-time.After(2 * time.Second):
			if h.cmd != nil && h.cmd.Process != nil {
				_ = h.cmd.Process.Kill()
			}
		case <-ctx.Done():
			err = ctx.Err()
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
	if h == nil || h.cmd == nil || h.cmd.Process == nil {
		return 0
	}
	return h.cmd.Process.Pid
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

func sidecarEnv(spec SidecarSpec) map[string]string {
	env := make(map[string]string, len(spec.Env)+1)
	for name, value := range spec.Env {
		env[name] = value
	}
	env["HOME"] = spec.Home
	return env
}
