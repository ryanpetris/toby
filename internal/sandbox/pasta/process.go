package pasta

// Lifecycle ownership for one foreground Pasta child process.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// Process is one running Pasta connection for a private network namespace.
type Process interface {
	// Done closes after Pasta exits and is reaped.
	Done() <-chan struct{}
	// Err returns the terminal Pasta process result.
	Err() error
	// Stop requests graceful Pasta termination.
	Stop(context.Context) error
	// Kill forcibly terminates Pasta.
	Kill(context.Context) error
}

type process struct {
	mu sync.Mutex

	command  *exec.Cmd
	output   *diagnosticOutput
	done     chan struct{}
	err      error
	stopping bool
}

var _ Process = (*process)(nil)

func newProcess(
	command *exec.Cmd,
	output *diagnosticOutput,
) *process {
	result := &process{
		command: command,
		output:  output,
		done:    make(chan struct{}),
	}
	go result.wait()

	return result
}

func (p *process) wait() {
	waitErr := p.command.Wait()

	p.mu.Lock()
	if !p.stopping {
		p.err = processError(waitErr, p.output.String())
	}
	p.command = nil
	close(p.done)
	p.mu.Unlock()
}

func processError(waitErr error, output string) error {
	output = strings.TrimSpace(output)
	switch {
	case waitErr != nil && output != "":
		return fmt.Errorf("wait for Pasta: %w: %s", waitErr, output)
	case waitErr != nil:
		return fmt.Errorf("wait for Pasta: %w", waitErr)
	case output != "":
		return fmt.Errorf("pasta exited: %s", output)
	default:
		return nil
	}
}

func (p *process) Done() <-chan struct{} {
	if p == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return p.done
}

func (p *process) Err() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *process) Stop(ctx context.Context) error {
	return p.signal(ctx, syscall.SIGTERM)
}

func (p *process) Kill(ctx context.Context) error {
	return p.signal(ctx, syscall.SIGKILL)
}

func (p *process) signal(
	ctx context.Context,
	signal os.Signal,
) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("signal Pasta: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.command == nil || p.command.Process == nil {
		p.stopping = true
		return nil
	}
	if err := p.command.Process.Signal(signal); err != nil &&
		!errors.Is(err, os.ErrProcessDone) {
		return fmt.Errorf("signal Pasta: %w", err)
	}
	p.stopping = true

	return nil
}
