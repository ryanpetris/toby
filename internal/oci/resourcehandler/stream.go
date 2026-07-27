package resourcehandler

// Replays one OCI preparation operation from its bounded live transcript.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config/ociresource"
)

const maximumLogRecordBytes = 128 << 10

type resourceStream struct {
	service       *Service
	resourceID    protocol.ResourceID
	configuration ociresource.Config
}

func (s *resourceStream) Follow(
	ctx context.Context,
	emit func(protocol.OCIEvent) error,
) error {
	if ctx == nil {
		return fmt.Errorf("OCI resource stream context is nil")
	}
	if emit == nil {
		return fmt.Errorf("OCI resource event emitter is nil")
	}

	current, err := s.service.operation(
		s.resourceID,
		s.configuration,
	)
	if err != nil {
		return err
	}
	defer current.release()
	progress, sequence, offset := current.attachment()
	if err := emit(protocol.OCIEvent{
		Kind:        protocol.OCIEventAccepted,
		OperationID: current.id,
	}); err != nil {
		return err
	}
	if progress != nil {
		if err := emit(protocol.OCIEvent{
			Kind:        protocol.OCIEventSnapshot,
			OperationID: current.id,
			Sequence:    sequence,
			Progress:    progress,
		}); err != nil {
			return err
		}
	}

	return followOperation(
		ctx,
		emit,
		current,
		offset,
	)
}

func (s *resourceStream) Close() error {
	return nil
}

func followOperation(
	ctx context.Context,
	emit func(protocol.OCIEvent) error,
	current *operation,
	offset int64,
) error {
	for {
		data, terminal, broken, notify := current.snapshot()
		size := int64(len(data))
		if offset < size {
			reader := bytes.NewReader(data[offset:size])
			scanner := bufio.NewScanner(reader)
			scanner.Buffer(
				make([]byte, 32<<10),
				maximumLogRecordBytes,
			)
			for scanner.Scan() {
				line := scanner.Bytes()
				offset += int64(len(line) + 1)

				var record logRecord
				if err := json.Unmarshal(line, &record); err != nil {
					return fmt.Errorf(
						"decode OCI operation log: %w",
						err,
					)
				}
				if err := writeRecord(
					emit,
					record,
				); err != nil {
					return err
				}
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read OCI operation log: %w", err)
			}
			if offset != size {
				return fmt.Errorf("OCI operation log ended mid-record")
			}
			continue
		}
		if terminal {
			if broken {
				return fmt.Errorf("OCI operation transcript failed")
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-notify:
		}
	}
}

func writeRecord(
	emit func(protocol.OCIEvent) error,
	record logRecord,
) error {
	event := protocol.OCIEvent{
		OperationID: record.OperationID,
		Sequence:    record.Sequence,
	}
	switch record.Kind {
	case recordProgress:
		if record.Progress == nil {
			return fmt.Errorf("OCI progress record is empty")
		}
		event.Kind = protocol.OCIEventProgress
		event.Progress = record.Progress
	case recordOutput:
		event.Kind = protocol.OCIEventOutput
		event.Source = record.Source
		event.Stream = record.Stream
		event.Data = append([]byte(nil), record.Data...)
	case recordComplete:
		event.Kind = protocol.OCIEventComplete
		event.Cached = record.Cached
	case recordFailed:
		event.Kind = protocol.OCIEventFailed
		event.Message = "OCI image preparation failed"
	default:
		return fmt.Errorf("unknown OCI operation record kind %q", record.Kind)
	}

	return emit(event)
}

var _ interface {
	Follow(context.Context, func(protocol.OCIEvent) error) error
	Close() error
} = (*resourceStream)(nil)
