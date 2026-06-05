// Package executil runs commands on the host as subprocesses. Runner.Run executes
// a command and returns its exit code, forwarding the host's stdio and signals;
// NewProcessRunner returns the default implementation.
package executil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

type Options struct {
	HideOutput bool
}

type Runner interface {
	Run(context.Context, []string, map[string]string, Options) (int, error)
}

type processRunner struct{}

var _ Runner = processRunner{}

// NewProcessRunner returns the default Runner, which executes commands as host
// subprocesses with signal forwarding.
func NewProcessRunner() Runner { return processRunner{} }

func (processRunner) Run(ctx context.Context, argv []string, env map[string]string, opts Options) (int, error) {
	if len(argv) == 0 {
		return 2, fmt.Errorf("missing command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = envList(env)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	var devNull *os.File
	if opts.HideOutput {
		var err error
		devNull, err = os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return 1, err
		}
		defer devNull.Close()
		cmd.Stdout = devNull
		cmd.Stderr = devNull
	}

	if err := cmd.Start(); err != nil {
		return startErrorCode(err), nil
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for sig := range signals {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(sig)
			}
		}
	}()

	err := cmd.Wait()
	signal.Stop(signals)
	close(signals)
	<-done
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	if errors.Is(err, context.Canceled) {
		return 130, nil
	}
	return 1, err
}

func envList(env map[string]string) []string {
	if env == nil {
		return os.Environ()
	}
	values := make([]string, 0, len(env))
	for name, value := range env {
		values = append(values, name+"="+value)
	}
	return values
}

func startErrorCode(err error) int {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return 127
	}
	if errors.Is(err, os.ErrPermission) {
		return 126
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if errors.Is(pathErr.Err, os.ErrNotExist) {
			return 127
		}
		if errors.Is(pathErr.Err, os.ErrPermission) {
			return 126
		}
	}
	if errors.Is(err, io.EOF) {
		return 1
	}
	return 126
}
