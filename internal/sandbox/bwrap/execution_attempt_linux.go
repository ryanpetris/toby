//go:build linux

package bwrap

// Prepares and runs one Bubblewrap execution attempt with payload provenance.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"petris.dev/toby/internal/diagnostic"
)

type executionAttempt struct {
	code           int
	err            error
	status         bubblewrapStatus
	statusErr      error
	payloadStarted bool
}

func (a executionAttempt) canRetry(ctx context.Context) bool {
	// Bubblewrap emits exit-code only after its CLOEXEC setup marker proves a
	// successful payload exec. A missing exit event is not sufficient alone:
	// require an ordinary setup-style exit, prior-run authority, and a live
	// request before replaying the invocation.
	return a.err == nil &&
		a.statusErr == nil &&
		a.code == 1 &&
		a.status.hasChildPID &&
		!a.status.hasExitCode &&
		!a.payloadStarted &&
		ctx.Err() == nil
}

func (a executionAttempt) result() (int, error) {
	if a.statusErr != nil {
		return a.code, errors.Join(a.err, a.statusErr)
	}
	if a.status.hasExitCode {
		if !a.status.hasChildPID {
			return a.code, errors.Join(
				a.err,
				fmt.Errorf(
					"bubblewrap reported payload exit without a child",
				),
			)
		}
		if a.status.exitCode != a.code {
			return a.code, errors.Join(
				a.err,
				fmt.Errorf(
					"bubblewrap payload status %d does not match process status %d",
					a.status.exitCode,
					a.code,
				),
			)
		}
		return a.code, a.err
	}
	if a.payloadStarted {
		return a.code, a.err
	}
	if a.status.hasChildPID {
		return a.code, errors.Join(
			a.err,
			fmt.Errorf(
				"bubblewrap exited with status %d before payload exec",
				a.code,
			),
		)
	}
	if a.err != nil {
		return a.code, a.err
	}
	return a.code, fmt.Errorf(
		"bubblewrap exited with status %d before reporting a child",
		a.code,
	)
}

func (e *Executor) executeAttempt(
	ctx context.Context,
	invocation *Invocation,
	streams ProcessIO,
	executable string,
	retryOutput *retryAttemptOutput,
	payloadTarget *payloadSignalTarget,
) (result executionAttempt) {
	var payloadSignals *payloadSignalRelay
	attemptInvocation, err := duplicateInvocation(invocation)
	if err != nil {
		if retryOutput != nil {
			retryOutput.abortPreparation()
		}
		return executionAttempt{code: 1, err: err}
	}
	defer func() {
		e.logger.DebugError(
			"close Bubblewrap attempt invocation",
			attemptInvocation.Close(),
		)
		if payloadSignals != nil {
			result.err = errors.Join(
				result.err,
				payloadSignals.close(),
			)
			result.payloadStarted = payloadSignals.payloadStarted()
		}
		if retryOutput != nil {
			result.payloadStarted = result.payloadStarted ||
				retryOutput.payloadStarted()
		}
	}()
	if retryOutput != nil {
		if err := retryOutput.prepare(attemptInvocation); err != nil {
			retryOutput.abortPreparation()
			return executionAttempt{
				code: 1,
				err:  err,
			}
		}
	}
	payloadSignals, err = newPayloadSignalRelay(
		streams.RegisterSignalHandler,
		payloadTarget,
	)
	if err != nil {
		return executionAttempt{code: 1, err: err}
	}
	if payloadSignals != nil {
		streams.RegisterSignalHandler = payloadSignals.registerHandler
		if err := payloadSignals.prepare(attemptInvocation); err != nil {
			return executionAttempt{code: 1, err: err}
		}
	}

	files := append([]*os.File(nil), attemptInvocation.ExtraFiles...)
	args := append([]string(nil), attemptInvocation.Args...)

	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		return executionAttempt{
			code: 1,
			err:  fmt.Errorf("create Bubblewrap status pipe: %w", err),
		}
	}
	statusWriterOpen := true
	defer func() {
		e.logger.DebugError(
			"close Bubblewrap status reader",
			statusReader.Close(),
		)
		if statusWriterOpen {
			e.logger.DebugError(
				"close Bubblewrap status writer",
				statusWriter.Close(),
			)
		}
	}()

	statusFD := childExtraFileBaseFD + len(files)
	files = append(files, statusWriter)

	gateReader, gateWriter, err := os.Pipe()
	if err != nil {
		return executionAttempt{
			code: 1,
			err:  fmt.Errorf("create Bubblewrap launch gate: %w", err),
		}
	}
	gateReaderOpen := true
	gateWriterOpen := true
	defer func() {
		if gateReaderOpen {
			e.logger.DebugError(
				"close Bubblewrap launch gate reader",
				gateReader.Close(),
			)
		}
		if gateWriterOpen {
			e.logger.DebugError(
				"close Bubblewrap launch gate writer",
				gateWriter.Close(),
			)
		}
	}()

	gateFD := childExtraFileBaseFD + len(files)
	files = append(files, gateReader)
	args = append(
		[]string{
			"--json-status-fd", strconv.Itoa(statusFD),
			"--block-fd", strconv.Itoa(gateFD),
		},
		args...,
	)
	command := exec.Command(executable, args...)
	command.ExtraFiles = files
	// The sandbox command receives its explicit --clearenv/--setenv contract.
	// Bubblewrap itself must not inherit host process configuration.
	command.Env = []string{}

	monitorStarted := make(chan int, 1)
	statusResult := trackForegroundStatus(
		statusReader,
		monitorStarted,
		gateWriter,
	)
	gateWriterOpen = false
	notifyStarted := func(pid int) {
		monitorStarted <- pid
	}

	switch attemptInvocation.Mode {
	case ExecutionNonInteractive:
		configureNonInteractive(command, streams)
		result.code, result.err = e.runStartedCommand(
			ctx,
			command,
			attemptInvocation,
			notifyStarted,
			streams.RegisterSignalHandler,
		)
	case ExecutionDirectTerminal:
		if err := configureDirectTerminal(command, streams); err != nil {
			result.code = 1
			result.err = err
			break
		}
		result.code, result.err = e.executeDirectTerminal(
			ctx,
			command,
			attemptInvocation,
			notifyStarted,
			streams.RegisterSignalHandler,
		)
	case ExecutionManagedPTY:
		result.code, result.err = e.executeManagedPTY(
			ctx,
			command,
			streams,
			attemptInvocation,
			notifyStarted,
			retryOutput,
		)
	default:
		result.code = 1
		result.err = fmt.Errorf(
			"invalid execution mode %q",
			attemptInvocation.Mode,
		)
	}
	close(monitorStarted)

	e.logger.DebugError(
		"close Bubblewrap launch gate reader",
		gateReader.Close(),
	)
	gateReaderOpen = false
	e.logger.DebugError(
		"close Bubblewrap status writer",
		statusWriter.Close(),
	)
	statusWriterOpen = false
	tracked := <-statusResult
	result.status = tracked.status
	result.statusErr = errors.Join(tracked.statusErr, tracked.sandboxErr)
	result.err = errors.Join(
		result.err,
		finalizeForegroundSandbox(
			ctx,
			tracked.sandbox,
			streams.NotifyFinalizing,
		),
	)

	return result
}

func duplicateInvocation(source *Invocation) (result *Invocation, returnErr error) {
	if source == nil {
		return nil, fmt.Errorf("rendered Bubblewrap invocation is nil")
	}

	duplicate := &Invocation{
		Args:                       append([]string(nil), source.Args...),
		Mode:                       source.Mode,
		payloadArgIndex:            source.payloadArgIndex,
		confidentialArguments:      source.confidentialArguments,
		confidentialArgumentsIndex: source.confidentialArgumentsIndex,
		allowOverlayReuseRetry:     source.allowOverlayReuseRetry,
	}
	defer func() {
		if returnErr != nil {
			diagnostic.DiscardError(
				"duplicating the Bubblewrap invocation already failed",
				"close partial Bubblewrap invocation duplicate",
				duplicate.Close(),
			)
			result = nil
		}
	}()
	if err := validateConfidentialArgumentReference(source); err != nil {
		return nil, err
	}
	for index, file := range source.ExtraFiles {
		var retained *os.File
		var err error
		if source.confidentialArguments &&
			index == source.confidentialArgumentsIndex {
			retained, err = duplicateConfidentialArgumentFile(file)
		} else {
			retained, err = duplicateDescriptor(
				file,
				fmt.Sprintf("invocation descriptor %d", index),
			)
		}
		if err != nil {
			return nil, err
		}
		duplicate.ExtraFiles = append(duplicate.ExtraFiles, retained)
	}

	return duplicate, nil
}
