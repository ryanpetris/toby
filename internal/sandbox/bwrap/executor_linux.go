//go:build linux

package bwrap

// Runs rendered Bubblewrap invocations as direct Linux child processes while
// preserving exit status, stdio separation, cancellation, and process-tree
// signal delivery.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/internal/diagnostic"
	sandboxapi "petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/shutdown"
)

const (
	defaultOverlayReuseRetryTimeout  = time.Second
	defaultOverlayReuseRetryInterval = 25 * time.Millisecond
)

// ExecutorOptions bounds graceful process-tree cancellation and the narrow
// retry window for a reused overlay whose previous mount is still detaching.
type ExecutorOptions struct {
	Executable                string
	TerminationGrace          time.Duration
	TerminationReapGrace      time.Duration
	OverlayReuseRetryTimeout  time.Duration
	OverlayReuseRetryInterval time.Duration
	ExternalInterrupts        bool
	Logger                    *diagnostic.Logger
}

// ProcessIO supplies one invocation's host-side streams and optional lifecycle
// callbacks. A managed PTY necessarily combines sandbox stdout and stderr on
// Stdout.
type ProcessIO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	RegisterPrompter      func(sandboxapi.ApprovalPrompter)
	RegisterSignalHandler func(
		func(syscall.Signal) error,
	) func()
	NotifyFinalizing func()
}

// Executor owns the resolved Bubblewrap executable path and the stable Linux
// parent thread used for background launches.
type Executor struct {
	mu sync.RWMutex

	executable         string
	backgroundLauncher *backgroundLauncher
	terminationGrace   time.Duration
	terminationReap    time.Duration
	retryTimeout       time.Duration
	retryInterval      time.Duration
	externalInterrupts bool
	logger             *diagnostic.Logger
}

var _ io.Closer = (*Executor)(nil)

// NewExecutor resolves the Bubblewrap executable used by subsequent
// invocations. Runtime compatibility is established by the real invocation.
func NewExecutor(options ExecutorOptions) (*Executor, error) {
	executable, err := resolveExecutable(options.Executable)
	if err != nil {
		return nil, fmt.Errorf("resolve Bubblewrap executable: %w", err)
	}

	grace := options.TerminationGrace
	if grace == 0 {
		grace = shutdown.SandboxTerminationGrace
	}
	if grace < 0 {
		return nil, fmt.Errorf("termination grace must be non-negative")
	}
	reapGrace := options.TerminationReapGrace
	if reapGrace == 0 {
		reapGrace = shutdown.SandboxReapGrace
	}
	if reapGrace < 0 {
		return nil, fmt.Errorf("termination reap grace must be non-negative")
	}
	retryTimeout := options.OverlayReuseRetryTimeout
	if retryTimeout == 0 {
		retryTimeout = defaultOverlayReuseRetryTimeout
	}
	if retryTimeout < 0 {
		return nil, fmt.Errorf(
			"overlay reuse retry timeout must be non-negative",
		)
	}
	retryInterval := options.OverlayReuseRetryInterval
	if retryInterval == 0 {
		retryInterval = defaultOverlayReuseRetryInterval
	}
	if retryInterval < 0 {
		return nil, fmt.Errorf(
			"overlay reuse retry interval must be non-negative",
		)
	}

	return &Executor{
		executable:         executable,
		backgroundLauncher: newBackgroundLauncher(),
		terminationGrace:   grace,
		terminationReap:    reapGrace,
		retryTimeout:       retryTimeout,
		retryInterval:      retryInterval,
		externalInterrupts: options.ExternalInterrupts,
		logger:             options.Logger,
	}, nil
}

// Close prevents new invocations and releases the background launch thread.
// The process-wide owner must close the Executor only after all Runs stop.
func (e *Executor) Close() error {
	if e == nil {
		return nil
	}

	e.mu.Lock()
	launcher := e.backgroundLauncher
	e.executable = ""
	e.backgroundLauncher = nil
	e.mu.Unlock()

	if launcher != nil {
		launcher.Close()
	}
	return nil
}

// Execute runs one rendered invocation. A matching Bubblewrap payload-status
// event, or a trusted payload-ready marker followed by an interrupted
// Bubblewrap monitor, makes a normal non-zero sandbox status a code with a nil
// error. Setup, start, transport, and cancellation failures return an error.
func (e *Executor) Execute(
	ctx context.Context,
	invocation *Invocation,
	streams ProcessIO,
) (code int, returnErr error) {
	var logger *diagnostic.Logger
	if e != nil {
		logger = e.logger
	}
	if invocation != nil {
		defer func() {
			logger.DebugError(
				"close Bubblewrap invocation",
				invocation.Close(),
			)
		}()
	}
	if e == nil {
		return 1, fmt.Errorf("bubblewrap executor is not configured")
	}
	if ctx == nil {
		return 1, fmt.Errorf("execute Bubblewrap invocation: context is nil")
	}
	if err := ctx.Err(); err != nil {
		return 1, err
	}
	if invocation == nil || len(invocation.Args) == 0 {
		return 1, fmt.Errorf("rendered Bubblewrap invocation is empty")
	}

	executable, err := e.executablePath()
	if err != nil {
		return 1, err
	}

	var payloadTarget *payloadSignalTarget
	if streams.RegisterSignalHandler != nil {
		payloadTarget = newPayloadSignalTarget()
		defer payloadTarget.Close()
	}

	if !invocation.allowOverlayReuseRetry {
		attempt := e.executeAttempt(
			ctx,
			invocation,
			streams,
			executable,
			nil,
			payloadTarget,
		)
		return attempt.result()
	}

	deadline := time.Now().Add(e.retryTimeout)
	for {
		output, err := newRetryAttemptOutput(streams, invocation.Mode)
		if err != nil {
			return 1, err
		}
		attempt := e.executeAttempt(
			ctx,
			invocation,
			output.streams,
			executable,
			output,
			payloadTarget,
		)
		if !attempt.canRetry(ctx) || !output.canRetry() {
			attempt.err = errors.Join(attempt.err, output.flush())
			return attempt.result()
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			attempt.err = errors.Join(attempt.err, output.flush())
			return attempt.result()
		}
		delay := min(e.retryInterval, remaining)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			attempt.err = errors.Join(attempt.err, output.flush())
			code, resultErr := attempt.result()
			return code, errors.Join(resultErr, ctx.Err())
		case <-timer.C:
		}
		if err := ctx.Err(); err != nil {
			attempt.err = errors.Join(attempt.err, output.flush())
			code, resultErr := attempt.result()
			return code, errors.Join(resultErr, err)
		}
		if err := output.discard(); err != nil {
			attempt.err = errors.Join(attempt.err, err)
			return attempt.result()
		}
	}
}

func (e *Executor) executablePath() (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.executable == "" {
		return "", fmt.Errorf("bubblewrap executor is closed")
	}
	return e.executable, nil
}

func (e *Executor) startBackgroundCommand(
	ctx context.Context,
	command *exec.Cmd,
) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.backgroundLauncher == nil {
		return fmt.Errorf("bubblewrap executor is closed")
	}

	return e.backgroundLauncher.Start(ctx, command)
}
