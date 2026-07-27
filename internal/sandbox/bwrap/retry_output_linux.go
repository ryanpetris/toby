//go:build linux

package bwrap

// Commits retry-attempt output at a trusted payload-start boundary while
// preserving every byte and bounding pre-payload buffering.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/layout"
)

const (
	maxPrePayloadOutput = 64 << 10
	payloadReadyByte    = byte(1)
)

type retryAttemptOutput struct {
	streams ProcessIO
	mode    ExecutionMode
	gate    *payloadOutputGate

	readyReader   *os.File
	readyWriter   *os.File
	payloadStderr *os.File
	watching      bool
	readyDone     chan struct{}
	ready         bool
	readyErr      error
}

func newRetryAttemptOutput(
	streams ProcessIO,
	mode ExecutionMode,
) (*retryAttemptOutput, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create payload-ready pipe: %w", err)
	}

	output := &retryAttemptOutput{
		streams:     streams,
		mode:        mode,
		gate:        newPayloadOutputGate(),
		readyReader: reader,
		readyWriter: writer,
		readyDone:   make(chan struct{}),
	}
	if mode != ExecutionManagedPTY {
		if err := output.gate.attach(streams.Stderr); err != nil {
			output.close()
			return nil, err
		}
		if stderr, ok := streams.Stderr.(*os.File); ok && stderr != nil {
			output.payloadStderr, err = duplicateDescriptor(
				stderr,
				"payload stderr",
			)
			if err != nil {
				output.close()
				return nil, err
			}
		}
		output.streams.Stderr = output.gate
	}
	output.watching = true
	go output.watchReady()

	return output, nil
}

func (o *retryAttemptOutput) prepare(invocation *Invocation) error {
	if o == nil || o.gate == nil ||
		o.readyReader == nil || o.readyWriter == nil {
		return fmt.Errorf("payload-ready output is not initialized")
	}
	if invocation == nil {
		return fmt.Errorf("payload-ready invocation is nil")
	}
	if invocation.payloadArgIndex <= 0 ||
		invocation.payloadArgIndex >= len(invocation.Args) {
		return fmt.Errorf("payload-ready invocation has no payload boundary")
	}

	readyFD := childExtraFileBaseFD + len(invocation.ExtraFiles)
	stderrFD := -1
	if o.payloadStderr != nil {
		stderrFD = readyFD + 1
	}
	payload := append(
		[]string(nil),
		invocation.Args[invocation.payloadArgIndex:]...,
	)
	arguments := append(
		[]string(nil),
		invocation.Args[:invocation.payloadArgIndex]...,
	)
	arguments = append(
		arguments,
		layout.SandboxBinary(),
		"exec",
		strconv.Itoa(readyFD),
		strconv.Itoa(stderrFD),
		"-1",
		"--",
	)
	invocation.Args = append(arguments, payload...)
	invocation.ExtraFiles = append(
		invocation.ExtraFiles,
		o.readyWriter,
	)
	o.readyWriter = nil
	if o.payloadStderr != nil {
		invocation.ExtraFiles = append(
			invocation.ExtraFiles,
			o.payloadStderr,
		)
		o.payloadStderr = nil
	}

	return nil
}

func (o *retryAttemptOutput) abortPreparation() {
	if o == nil {
		return
	}

	if o.readyWriter != nil {
		diagnostic.DiscardError(
			"aborting output preparation already determines the attempt result",
			"close payload-ready writer",
			o.readyWriter.Close(),
		)
		o.readyWriter = nil
	}
	if o.payloadStderr != nil {
		diagnostic.DiscardError(
			"aborting output preparation already determines the attempt result",
			"close payload stderr descriptor",
			o.payloadStderr.Close(),
		)
		o.payloadStderr = nil
	}
}

func (o *retryAttemptOutput) attachManagedSink(output io.Writer) error {
	if o == nil || o.mode != ExecutionManagedPTY {
		return nil
	}

	return o.gate.attach(output)
}

func (o *retryAttemptOutput) failures() <-chan error {
	if o == nil || o.gate == nil {
		return nil
	}

	return o.gate.failures
}

func (o *retryAttemptOutput) canRetry() bool {
	if o == nil {
		return false
	}

	<-o.readyDone
	return !o.ready &&
		o.readyErr == nil &&
		o.gate.replayAllowed()
}

func (o *retryAttemptOutput) payloadStarted() bool {
	if o == nil {
		return false
	}

	<-o.readyDone
	return o.ready
}

func (o *retryAttemptOutput) flush() error {
	if o == nil {
		return nil
	}

	<-o.readyDone
	resultErr := errors.Join(
		o.readyErr,
		o.gate.finish(true),
	)
	o.close()
	return resultErr
}

func (o *retryAttemptOutput) discard() error {
	if o == nil {
		return nil
	}

	<-o.readyDone
	resultErr := errors.Join(
		o.readyErr,
		o.gate.finish(false),
	)
	o.close()
	return resultErr
}

func (o *retryAttemptOutput) close() {
	if o == nil {
		return
	}

	if !o.watching && o.readyReader != nil {
		diagnostic.DiscardError(
			"closing an unused payload-ready reader is cleanup",
			"close payload-ready reader",
			o.readyReader.Close(),
		)
		o.readyReader = nil
	}
	if o.readyWriter != nil {
		diagnostic.DiscardError(
			"closing an unused payload-ready writer is cleanup",
			"close payload-ready writer",
			o.readyWriter.Close(),
		)
		o.readyWriter = nil
	}
	if o.payloadStderr != nil {
		diagnostic.DiscardError(
			"closing an unused payload stderr descriptor is cleanup",
			"close payload stderr descriptor",
			o.payloadStderr.Close(),
		)
		o.payloadStderr = nil
	}
}

func (o *retryAttemptOutput) watchReady() {
	defer close(o.readyDone)
	defer func() {
		diagnostic.DiscardError(
			"closing the consumed readiness pipe cannot change the marker result",
			"close payload-ready reader",
			o.readyReader.Close(),
		)
	}()

	var marker [1]byte
	count, err := io.ReadFull(o.readyReader, marker[:])
	if err == io.EOF && count == 0 {
		return
	}
	if err != nil {
		o.readyErr = fmt.Errorf("read payload-ready marker: %w", err)
		o.gate.rejectReplay(o.readyErr)
		return
	}
	if marker[0] != payloadReadyByte {
		o.readyErr = fmt.Errorf(
			"payload-ready marker is %d, want %d",
			marker[0],
			payloadReadyByte,
		)
		o.gate.rejectReplay(o.readyErr)
		return
	}

	o.ready = true
	if err := o.gate.commit(); err != nil {
		o.readyErr = err
	}
}
