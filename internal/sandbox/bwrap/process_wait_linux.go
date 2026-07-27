//go:build linux

package bwrap

// Waits for Bubblewrap process groups and performs bounded signal termination.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"petris.dev/toby/internal/diagnostic"
)

func (e *Executor) runStartedCommand(
	ctx context.Context,
	command *exec.Cmd,
	invocation *Invocation,
	notifyStarted func(int),
	registerSignalHandler func(func(syscall.Signal) error) func(),
) (int, error) {
	if err := command.Start(); err != nil {
		return 1, fmt.Errorf("start Bubblewrap: %w", err)
	}
	if notifyStarted != nil {
		notifyStarted(command.Process.Pid)
	}
	group, err := retainStartedProcessGroup(command, invocation)
	if err != nil {
		return 1, err
	}
	defer func() {
		e.logger.DebugError(
			"close Bubblewrap process-group identity",
			group.Close(),
		)
	}()

	e.logger.DebugError(
		"close started Bubblewrap invocation",
		invocation.Close(),
	)

	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	waitErr, interruptedErr := e.waitForProcess(
		ctx,
		group,
		wait,
		registerSignalHandler,
	)
	code, resultErr := childResult(waitErr)

	return code, errors.Join(interruptedErr, resultErr)
}

func (e *Executor) waitForProcess(
	ctx context.Context,
	group *processGroupIdentity,
	wait <-chan error,
	registerSignalHandler func(func(syscall.Signal) error) func(),
) (waitErr error, returnErr error) {
	select {
	case waitErr = <-wait:
		return waitErr, nil
	default:
	}

	forwarded := make(chan os.Signal, 4)
	localSignals := []os.Signal{syscall.SIGHUP, syscall.SIGQUIT}
	if !e.externalInterrupts {
		localSignals = append(
			localSignals,
			syscall.SIGINT,
			syscall.SIGTERM,
		)
	}
	signal.Notify(forwarded, localSignals...)
	defer signal.Stop(forwarded)
	unregister := registerProcessSignalHandler(
		registerSignalHandler,
		group,
	)
	defer unregister()

	for {
		select {
		case waitErr = <-wait:
			return waitErr, returnErr
		case current := <-forwarded:
			sig, ok := current.(syscall.Signal)
			if !ok {
				continue
			}
			if err := group.Signal(sig); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		case <-ctx.Done():
			cancelErr := e.terminateCommand(
				group,
				wait,
			)
			return cancelErr.waitErr, errors.Join(
				returnErr,
				ctx.Err(),
				cancelErr.signalErr,
			)
		}
	}
}

type terminationResult struct {
	waitErr   error
	signalErr error
}

func (e *Executor) terminateCommand(
	group *processGroupIdentity,
	wait <-chan error,
) terminationResult {
	grace := e.terminationGrace
	signalErr := group.Signal(syscall.SIGTERM)
	if grace == 0 {
		signalErr = errors.Join(
			signalErr,
			group.Signal(syscall.SIGKILL),
		)
		return terminationResult{
			waitErr:   waitForTermination(wait, e.terminationReap),
			signalErr: signalErr,
		}
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case waitErr := <-wait:
		return terminationResult{waitErr: waitErr, signalErr: signalErr}
	case <-timer.C:
		signalErr = errors.Join(
			signalErr,
			group.Signal(syscall.SIGKILL),
		)
		return terminationResult{
			waitErr:   waitForTermination(wait, e.terminationReap),
			signalErr: signalErr,
		}
	}
}

func waitForTermination(
	wait <-chan error,
	grace time.Duration,
) error {
	if grace == 0 {
		select {
		case err := <-wait:
			return err
		default:
			return fmt.Errorf("sandbox process did not exit after SIGKILL")
		}
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case err := <-wait:
		return err
	case <-timer.C:
		return fmt.Errorf(
			"sandbox process did not exit within %s after SIGKILL",
			grace,
		)
	}
}

func registerProcessSignalHandler(
	register func(func(syscall.Signal) error) func(),
	group *processGroupIdentity,
) func() {
	if register == nil {
		return func() {}
	}
	return register(group.Signal)
}

func retainStartedProcessGroup(
	command *exec.Cmd,
	invocation *Invocation,
) (*processGroupIdentity, error) {
	if command == nil || command.Process == nil {
		return nil, fmt.Errorf("started Bubblewrap command has no process")
	}

	group, err := openProcessGroupIdentity(
		command.Process.Pid,
		os.Getpid(),
	)
	if err == nil {
		return group, nil
	}

	// No Wait has run, so Process.Kill still targets this exact unreaped child
	// by its positive PID. Never fall back to a numeric process-group signal.
	killErr := command.Process.Kill()
	if errors.Is(killErr, os.ErrProcessDone) ||
		errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	diagnostic.DiscardError(
		"retaining the Bubblewrap process group already failed",
		"close started Bubblewrap invocation",
		invocation.Close(),
	)
	waitErr := command.Wait()
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		waitErr = nil
	}

	return nil, errors.Join(
		fmt.Errorf(
			"retain exact Bubblewrap process-group identity: %w",
			err,
		),
		killErr,
		waitErr,
	)
}

func childResult(err error) (int, error) {
	if err == nil {
		return 0, nil
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		return 1, err
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok {
		return exitError.ExitCode(), nil
	}
	if status.Signaled() {
		return 128 + int(status.Signal()), nil
	}

	return status.ExitStatus(), nil
}
