//go:build linux

package bwrap

// Allocates and drives a host PTY for managed foreground commands, preserving
// terminal sizing, raw input, EOF, output draining, and process-group cleanup.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
	"golang.org/x/term"

	"petris.dev/toby/internal/diagnostic"
)

const (
	defaultTerminalColumns = 80
	defaultTerminalRows    = 24
)

func (e *Executor) executeManagedPTY(
	ctx context.Context,
	command *exec.Cmd,
	streams ProcessIO,
	invocation *Invocation,
	notifyStarted func(int),
	retryOutput *retryAttemptOutput,
) (code int, returnErr error) {
	size := initialPTYSize(streams.Stdout)
	master, err := pty.StartWithSize(command, size)
	if err != nil {
		return 1, fmt.Errorf("start Bubblewrap with managed PTY: %w", err)
	}
	if notifyStarted != nil {
		notifyStarted(command.Process.Pid)
	}
	defer func() {
		e.logger.DebugError("close managed PTY descriptor", master.Close())
	}()
	group, err := retainStartedProcessGroup(command, invocation)
	if err != nil {
		return 1, err
	}
	defer func() {
		e.logger.DebugError(
			"close managed-terminal process-group identity",
			group.Close(),
		)
	}()

	var state *managedTerminalState
	forwarder := startManagedSignalForwarder(
		group,
		e.externalInterrupts,
		streams.RegisterSignalHandler,
	)
	defer func() {
		stateErr := state.Close()
		forwarderErr := forwarder.Close()
		// Restoring the terminal and completing signal delivery are required
		// finalization because either can change the foreground result.
		returnErr = errors.Join(returnErr, stateErr, forwarderErr)
	}()

	e.logger.DebugError(
		"close managed-terminal Bubblewrap invocation",
		invocation.Close(),
	)

	wait := make(chan error, 1)
	go func() {
		wait <- command.Wait()
	}()

	outputDone := make(chan error, 1)
	var failures <-chan error
	var inputFailures <-chan error
	hostInput, hostOutput, interactive := terminalFiles(streams)
	if interactive {
		state, err = newManagedTerminalState(
			hostInput,
			hostOutput,
			master,
			group,
		)
		if err != nil {
			terminated := e.terminateCommand(
				group,
				wait,
			)
			return 1, errors.Join(
				err,
				terminated.waitErr,
				terminated.signalErr,
			)
		}

		width, height := terminalSize(hostOutput)
		foreground, err := newTerminalForeground(
			hostInput,
			hostOutput,
			master,
			streams.RegisterPrompter,
			state.Suspend,
			state.Resume,
			state.SuspendCharacter,
			width,
			height,
		)
		if err != nil {
			terminated := e.terminateCommand(
				group,
				wait,
			)
			return 1, errors.Join(
				err,
				terminated.waitErr,
				terminated.signalErr,
			)
		}
		defer func() {
			returnErr = errors.Join(returnErr, foreground.Close())
		}()
		failures = foreground.Failures()

		if err := resizeManagedPTY(master, hostOutput); err != nil {
			terminated := e.terminateCommand(
				group,
				wait,
			)
			return 1, errors.Join(
				err,
				terminated.waitErr,
				terminated.signalErr,
			)
		}
		stopResize := forwardTerminalResize(
			master,
			hostOutput,
			foreground.Resize,
			foreground.reportFailure,
		)
		defer stopResize()

		outputSink := io.Writer(managedForegroundOutput{
			foreground: foreground,
			fallback:   hostOutput,
		})
		if retryOutput != nil {
			if err := retryOutput.attachManagedSink(outputSink); err != nil {
				terminated := e.terminateCommand(
					group,
					wait,
				)
				return 1, errors.Join(
					err,
					terminated.waitErr,
					terminated.signalErr,
				)
			}
			outputSink = retryOutput.gate
		}
		go func() {
			outputDone <- pumpManagedPTYOutput(master, outputSink)
		}()
	} else {
		input, inputErr := startPTYInput(master, streams.Stdin)
		if inputErr != nil {
			terminated := e.terminateCommand(
				group,
				wait,
			)
			return 1, errors.Join(
				inputErr,
				terminated.waitErr,
				terminated.signalErr,
			)
		}
		defer func() {
			if input != nil {
				returnErr = errors.Join(returnErr, input.Close())
			}
		}()
		if input != nil {
			inputFailures = input.Failures()
		}

		output := streams.Stdout
		if output == nil {
			output = io.Discard
		}
		if retryOutput != nil {
			if err := retryOutput.attachManagedSink(output); err != nil {
				terminated := e.terminateCommand(
					group,
					wait,
				)
				return 1, errors.Join(
					err,
					terminated.waitErr,
					terminated.signalErr,
				)
			}
			output = retryOutput.gate
		}
		go func() {
			outputDone <- pumpManagedPTYOutput(master, output)
		}()
	}

	waitErr, outputErr, interruptedErr := e.waitForManagedProcess(
		ctx,
		group,
		wait,
		outputDone,
		failures,
		inputFailures,
		retryOutput.failures(),
	)
	code, resultErr := childResult(waitErr)

	return code, errors.Join(
		interruptedErr,
		resultErr,
		outputErr,
	)
}

func initialPTYSize(output io.Writer) *pty.Winsize {
	columns, rows := defaultTerminalColumns, defaultTerminalRows
	if file, ok := output.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		width, height, err := term.GetSize(int(file.Fd()))
		if err == nil && width > 0 && height > 0 {
			columns, rows = width, height
		}
	}

	return &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(columns),
	}
}

func (e *Executor) waitForManagedProcess(
	ctx context.Context,
	group *processGroupIdentity,
	wait <-chan error,
	outputDone <-chan error,
	failures <-chan error,
	inputFailures <-chan error,
	retryFailures <-chan error,
) (waitErr, outputErr, returnErr error) {
	output := outputDone
	for {
		select {
		case waitErr = <-wait:
			if output != nil {
				outputErr = <-output
			}
			return waitErr, outputErr, returnErr
		case outputErr = <-output:
			output = nil
			if outputErr == nil {
				continue
			}

			terminated := e.terminateCommand(
				group,
				wait,
			)
			return terminated.waitErr, outputErr, errors.Join(
				returnErr,
				terminated.signalErr,
			)
		case failure := <-failures:
			terminated := e.terminateCommand(
				group,
				wait,
			)
			if output != nil {
				outputErr = <-output
			}
			return terminated.waitErr, outputErr, errors.Join(
				returnErr,
				failure,
				terminated.signalErr,
			)
		case <-inputFailures:
			terminated := e.terminateCommand(
				group,
				wait,
			)
			if output != nil {
				outputErr = <-output
			}
			return terminated.waitErr, outputErr, errors.Join(
				returnErr,
				terminated.signalErr,
			)
		case failure := <-retryFailures:
			terminated := e.terminateCommand(
				group,
				wait,
			)
			if output != nil {
				outputErr = <-output
			}
			return terminated.waitErr, outputErr, errors.Join(
				returnErr,
				failure,
				terminated.signalErr,
			)
		case <-ctx.Done():
			terminated := e.terminateCommand(
				group,
				wait,
			)
			if output != nil {
				outputErr = <-output
			}
			return terminated.waitErr, outputErr, errors.Join(
				returnErr,
				ctx.Err(),
				terminated.signalErr,
			)
		}
	}
}

type managedForegroundOutput struct {
	foreground *terminalForeground
	fallback   io.Writer
}

var _ io.Writer = managedForegroundOutput{}

func (o managedForegroundOutput) Write(data []byte) (int, error) {
	if o.foreground == nil {
		return 0, fmt.Errorf("managed terminal foreground is nil")
	}
	if err := o.foreground.onOutput(data); err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return o.fallback.Write(data)
		}
		return 0, err
	}

	return len(data), nil
}

func pumpManagedPTYOutput(master io.Reader, output io.Writer) error {
	buffer := make([]byte, 32*1024)
	for {
		count, readErr := master.Read(buffer)
		if count > 0 {
			if err := writeTerminalBytes(output, buffer[:count]); err != nil {
				return fmt.Errorf("write managed PTY output: %w", err)
			}
		}
		if readErr != nil {
			return normalizeManagedPTYMasterReadError(readErr)
		}
	}
}

func normalizeManagedPTYMasterReadError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, syscall.EIO) {
		return nil
	}
	return fmt.Errorf("read managed PTY output: %w", err)
}

type managedPTYInput struct {
	reader cancelreader.CancelReader
	done   chan struct{}
	failed chan error
	err    error
}

func startPTYInput(
	master *os.File,
	input io.Reader,
) (*managedPTYInput, error) {
	if input == nil {
		return nil, nil
	}
	reader, err := cancelreader.NewReader(input)
	if err != nil {
		return nil, fmt.Errorf("make managed PTY input cancelable: %w", err)
	}

	pump := &managedPTYInput{
		reader: reader,
		done:   make(chan struct{}),
		failed: make(chan error, 1),
	}
	go func() {
		defer close(pump.done)
		pump.err = pumpManagedPTYInput(master, reader)
		pump.reportFailure(pump.err)
	}()

	return pump, nil
}

func (p *managedPTYInput) Close() error {
	if p == nil {
		return nil
	}

	p.reader.Cancel()
	<-p.done
	diagnostic.DiscardError(
		"closing the canceled managed PTY input reader is cleanup",
		"close managed PTY input reader",
		p.reader.Close(),
	)
	return normalizeManagedPTYInputError(p.err)
}

func (p *managedPTYInput) Failures() <-chan error {
	if p == nil {
		return nil
	}
	return p.failed
}

func (p *managedPTYInput) reportFailure(err error) {
	err = normalizeManagedPTYInputError(err)
	if err == nil {
		return
	}
	select {
	case p.failed <- err:
	default:
	}
}

func pumpManagedPTYInput(master io.Writer, input io.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		count, readErr := input.Read(buffer)
		if count > 0 {
			if err := writeTerminalBytes(master, buffer[:count]); err != nil {
				return normalizeManagedPTYMasterWriteError(err)
			}
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			if errors.Is(readErr, cancelreader.ErrCanceled) {
				return readErr
			}
			return fmt.Errorf("read managed PTY input: %w", readErr)
		}

		// A PTY has no independently closable input half. Feed the terminal's
		// canonical EOF byte without closing the master and truncating output.
		return normalizeManagedPTYMasterWriteError(
			writeTerminalBytes(master, []byte{0x04}),
		)
	}
}

func normalizeManagedPTYMasterWriteError(err error) error {
	if err == nil ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, syscall.EIO) {
		return nil
	}
	return fmt.Errorf("write managed PTY input: %w", err)
}

func normalizeManagedPTYInputError(err error) error {
	if errors.Is(err, cancelreader.ErrCanceled) {
		return nil
	}
	return err
}

func terminalFiles(streams ProcessIO) (
	input *os.File,
	output *os.File,
	ok bool,
) {
	input, inputOK := streams.Stdin.(*os.File)
	output, outputOK := streams.Stdout.(*os.File)
	if !inputOK || !outputOK {
		return nil, nil, false
	}
	if !term.IsTerminal(int(input.Fd())) ||
		!term.IsTerminal(int(output.Fd())) {
		return nil, nil, false
	}

	return input, output, true
}

func resizeManagedPTY(master, hostOutput *os.File) error {
	width, height := terminalSize(hostOutput)
	return setManagedPTYSize(master, width, height)
}

func setManagedPTYSize(master *os.File, width, height int) error {
	if err := pty.Setsize(master, &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	}); err != nil {
		return fmt.Errorf("resize managed PTY: %w", err)
	}
	return nil
}

func terminalSize(hostOutput *os.File) (width, height int) {
	width, height, err := term.GetSize(int(hostOutput.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		return defaultTerminalColumns, defaultTerminalRows
	}

	return width, height
}

func forwardTerminalResize(
	master, hostOutput *os.File,
	resized func(int, int),
	failed func(error),
) func() {
	resizes := make(chan os.Signal, 1)
	signal.Notify(resizes, syscall.SIGWINCH)
	done := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		for {
			select {
			case <-resizes:
				if err := resizeManagedPTY(master, hostOutput); err != nil {
					if failed != nil {
						failed(err)
					}
					continue
				}
				if resized != nil {
					width, height := terminalSize(hostOutput)
					resized(width, height)
				}
			case <-done:
				return
			}
		}
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			signal.Stop(resizes)
			close(done)
			<-finished
		})
	}
}
