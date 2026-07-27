package resourcehandler

// Appends bounded OCI operation records and wakes disk-backed followers.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/oci"
)

type recordKind string

const (
	recordProgress recordKind = "progress"
	recordOutput   recordKind = "output"
	recordComplete recordKind = "complete"
	recordFailed   recordKind = "failed"
)

type logRecord struct {
	OperationID protocol.OperationID       `json:"operation_id"`
	Sequence    uint64                     `json:"sequence"`
	Kind        recordKind                 `json:"kind"`
	Source      protocol.OCISource         `json:"source,omitempty"`
	Stream      protocol.OutputStream      `json:"stream,omitempty"`
	Progress    *protocol.OCIProgressState `json:"progress,omitempty"`
	Data        []byte                     `json:"data,omitempty"`
	Cached      bool                       `json:"cached,omitempty"`
}

type operation struct {
	id       protocol.OperationID
	file     *os.File
	maxBytes int64
	logger   *diagnostic.Logger

	mu        sync.Mutex
	data      []byte
	sequence  uint64
	terminal  bool
	broken    bool
	truncated bool
	worked    bool
	followers int
	producing bool
	notify    chan struct{}

	progress         *protocol.OCIProgressState
	progressSequence uint64
	progressOffset   int64
}

func newOperation(
	id protocol.OperationID,
	file *os.File,
	maxBytes int64,
	logger *diagnostic.Logger,
) *operation {
	return &operation{
		id:        id,
		file:      file,
		maxBytes:  maxBytes,
		logger:    logger,
		producing: true,
		notify:    make(chan struct{}),
	}
}

func (o *operation) retain() {
	o.mu.Lock()
	o.followers++
	o.mu.Unlock()
}

func (o *operation) release() {
	o.mu.Lock()
	if o.followers > 0 {
		o.followers--
	}
	file := o.closeIfUnusedLocked()
	o.mu.Unlock()
	if file != nil {
		o.logger.DebugError("close OCI operation log", file.Close())
	}
}

func (o *operation) producerDone() {
	o.mu.Lock()
	o.producing = false
	file := o.closeIfUnusedLocked()
	o.mu.Unlock()
	if file != nil {
		o.logger.DebugError("close OCI operation log", file.Close())
	}
}

func (o *operation) report(progress oci.Progress) error {
	state, err := protocolProgress(progress)
	if err != nil {
		return err
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal {
		return errors.New("OCI operation is unavailable")
	}
	if o.progress != nil && *o.progress == state {
		return nil
	}

	o.worked = true
	written := o.appendLocked(logRecord{
		Kind:     recordProgress,
		Progress: &state,
	}, false)
	if o.broken {
		return errors.New("encode OCI operation transcript")
	}
	if !written {
		snapshot := state
		o.progress = &snapshot
	}

	return nil
}

func (o *operation) writer(
	source protocol.OCISource,
	stream protocol.OutputStream,
) io.Writer {
	return operationOutputWriter{
		operation: o,
		source:    source,
		stream:    stream,
	}
}

type operationOutputWriter struct {
	operation *operation
	source    protocol.OCISource
	stream    protocol.OutputStream
}

var _ io.Writer = operationOutputWriter{}

func (w operationOutputWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	w.operation.mu.Lock()
	defer w.operation.mu.Unlock()
	if w.operation.terminal {
		return 0, io.ErrClosedPipe
	}
	w.operation.worked = true
	w.operation.appendOutputLocked(
		w.source,
		w.stream,
		append([]byte(nil), data...),
	)
	if w.operation.broken {
		return 0, errors.New("encode OCI operation output")
	}
	return len(data), nil
}

func protocolProgress(input oci.Progress) (
	protocol.OCIProgressState,
	error,
) {
	var phase protocol.OCIProgressPhase
	switch input.Phase {
	case oci.ProgressResolving:
		phase = protocol.OCIProgressResolving
	case oci.ProgressDownloading:
		phase = protocol.OCIProgressDownloading
	case oci.ProgressExtracting:
		phase = protocol.OCIProgressExtracting
	default:
		return protocol.OCIProgressState{}, fmt.Errorf(
			"unknown OCI progress phase %q",
			input.Phase,
		)
	}
	if input.CompletedBytes < 0 ||
		input.TotalBytes < 0 ||
		input.CompletedItems < 0 ||
		input.TotalItems < 0 {
		return protocol.OCIProgressState{}, fmt.Errorf(
			"OCI progress values must not be negative",
		)
	}
	if input.TotalBytes != 0 &&
		input.CompletedBytes > input.TotalBytes {
		return protocol.OCIProgressState{}, fmt.Errorf(
			"OCI completed bytes exceed total bytes",
		)
	}
	if input.TotalItems != 0 &&
		input.CompletedItems > input.TotalItems {
		return protocol.OCIProgressState{}, fmt.Errorf(
			"OCI completed items exceed total items",
		)
	}

	return protocol.OCIProgressState{
		Phase:          phase,
		CompletedBytes: input.CompletedBytes,
		TotalBytes:     input.TotalBytes,
		CompletedItems: input.CompletedItems,
		TotalItems:     input.TotalItems,
	}, nil
}

func (o *operation) complete() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal {
		return
	}

	o.appendLocked(logRecord{
		Kind:   recordComplete,
		Source: protocol.OCISourceCache,
		Cached: !o.worked,
	}, true)
}

func (o *operation) fail(
	operationErr error,
	source protocol.OCISource,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.terminal {
		return
	}

	if operationErr != nil {
		if o.progress != nil &&
			o.progress.Phase == protocol.OCIProgressExtracting {
			source = protocol.OCISourceExtractor
		}
		o.appendOutputLocked(
			source,
			protocol.OutputStderr,
			[]byte(operationErr.Error()+"\n"),
		)
	}
	o.appendLocked(logRecord{
		Kind:   recordFailed,
		Source: protocol.OCISourceCache,
	}, true)
}

func (o *operation) appendOutputLocked(
	source protocol.OCISource,
	stream protocol.OutputStream,
	data []byte,
) {
	for len(data) > 0 {
		size := len(data)
		if size > protocol.MaxProgressOutputBytes {
			size = protocol.MaxProgressOutputBytes
		}

		if !o.terminal && !o.truncated {
			o.appendLocked(logRecord{
				Kind:   recordOutput,
				Source: source,
				Stream: stream,
				Data:   append([]byte(nil), data[:size]...),
			}, false)
		}

		data = data[size:]
	}
}

func (o *operation) appendLocked(
	record logRecord,
	terminal bool,
) bool {
	if o.terminal {
		return false
	}

	record.OperationID = o.id
	record.Sequence = o.sequence + 1
	encoded, err := json.Marshal(record)
	if err != nil {
		o.finishBrokenLocked()
		return false
	}
	encoded = append(encoded, '\n')

	if !terminal &&
		int64(len(o.data)+len(encoded)) > o.maxBytes {
		if record.Kind == recordOutput {
			o.truncated = true
		}
		return false
	}

	offset := len(o.data)
	o.data = append(o.data, encoded...)
	if o.file != nil {
		written, writeErr := o.file.WriteAt(encoded, int64(offset))
		if writeErr != nil || written != len(encoded) {
			if writeErr == nil {
				writeErr = fmt.Errorf(
					"short write: wrote %d of %d bytes",
					written,
					len(encoded),
				)
			}
			o.logger.DebugError(
				"write OCI operation log",
				writeErr,
				"operation_id",
				o.id,
			)
			o.logger.DebugError(
				"close failed OCI operation log",
				o.file.Close(),
				"operation_id",
				o.id,
			)
			o.file = nil
		}
	}
	o.sequence++
	if record.Kind == recordProgress && record.Progress != nil {
		snapshot := *record.Progress
		o.progress = &snapshot
		o.progressSequence = o.sequence
		o.progressOffset = int64(len(o.data))
	}
	if terminal {
		if o.file != nil {
			if err := o.file.Sync(); err != nil {
				o.logger.DebugError(
					"sync OCI operation log",
					err,
					"operation_id",
					o.id,
				)
			}
		}
		o.terminal = true
	}
	o.signalLocked()

	return true
}

func (o *operation) attachment() (
	progress *protocol.OCIProgressState,
	sequence uint64,
	offset int64,
) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.progress == nil || o.progressSequence == 0 {
		return nil, 0, 0
	}
	snapshot := *o.progress
	return &snapshot, o.progressSequence, o.progressOffset
}

func (o *operation) signalLocked() {
	close(o.notify)
	o.notify = make(chan struct{})
}

func (o *operation) finishBrokenLocked() {
	o.broken = true
	o.terminal = true
	o.signalLocked()
}

func (o *operation) snapshot() (
	data []byte,
	terminal bool,
	broken bool,
	notify <-chan struct{},
) {
	o.mu.Lock()
	defer o.mu.Unlock()

	return o.data, o.terminal, o.broken, o.notify
}

func (o *operation) close() error {
	if o == nil {
		return nil
	}

	o.mu.Lock()
	file := o.file
	o.file = nil
	if !o.terminal {
		o.terminal = true
		o.signalLocked()
	}
	o.mu.Unlock()
	if file == nil {
		return nil
	}

	return file.Close()
}

func (o *operation) closeIfUnusedLocked() *os.File {
	if o.producing || o.followers != 0 {
		return nil
	}

	file := o.file
	o.file = nil
	return file
}
