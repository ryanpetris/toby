package client

// Receives typed OCI preparation events from one lease-authorized stream.

import (
	"context"
	"errors"
	"fmt"
	"io"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// OCIEventStream receives one shared agent preparation operation.
type OCIEventStream struct {
	stream agentv1.AgentService_PrepareOCIClient
	id     protocol.CorrelationID
	cancel context.CancelFunc
}

// PrepareOCI starts or joins OCI preparation for one active lease.
func (s *AgentSession) PrepareOCI(
	ctx context.Context,
	lease *ResourceLease,
) (*OCIEventStream, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return nil, err
	}
	if lease == nil || lease.session != s {
		return nil, fmt.Errorf(
			"OCI resource lease does not belong to this agent session",
		)
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := s.client.PrepareOCI(
		streamCtx,
		&agentv1.OCIPrepareRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
			ResourceId:    string(lease.resourceID),
			LeaseId:       string(lease.leaseID),
		},
	)
	if err != nil {
		cancel()
		return nil, remoteRequestError(err, id)
	}

	return &OCIEventStream{
		stream: stream,
		id:     id,
		cancel: cancel,
	}, nil
}

// Recv receives and validates one preparation event.
func (s *OCIEventStream) Recv() (protocol.OCIEvent, error) {
	if s == nil || s.stream == nil {
		return protocol.OCIEvent{}, io.ErrClosedPipe
	}

	message, err := s.stream.Recv()
	if err != nil {
		return protocol.OCIEvent{}, remoteRequestError(err, s.id)
	}
	if err := requireCorrelation(message.GetCorrelationId(), s.id); err != nil {
		return protocol.OCIEvent{}, err
	}

	var event protocol.OCIEvent
	switch {
	case message.GetAccepted() != nil:
		accepted := message.GetAccepted()
		event.Kind = protocol.OCIEventAccepted
		event.OperationID = protocol.OperationID(
			accepted.GetOperationId(),
		)
	case message.GetSnapshot() != nil:
		snapshot := message.GetSnapshot()
		progress, err := protocol.OCIProgressFromAgent(
			snapshot.GetProgress(),
		)
		if err != nil {
			return protocol.OCIEvent{}, err
		}
		event.Kind = protocol.OCIEventSnapshot
		event.OperationID = protocol.OperationID(
			snapshot.GetOperationId(),
		)
		event.Sequence = snapshot.GetSequence()
		event.Progress = &progress
	case message.GetProgress() != nil:
		update := message.GetProgress()
		progress, err := protocol.OCIProgressFromAgent(
			update.GetProgress(),
		)
		if err != nil {
			return protocol.OCIEvent{}, err
		}
		event.Kind = protocol.OCIEventProgress
		event.OperationID = protocol.OperationID(update.GetOperationId())
		event.Sequence = update.GetSequence()
		event.Progress = &progress
	case message.GetOutput() != nil:
		output := message.GetOutput()
		source, err := protocol.OCISourceFromAgent(output.GetSource())
		if err != nil {
			return protocol.OCIEvent{}, err
		}
		outputStream, err := protocol.OutputStreamFromAgent(
			output.GetStream(),
		)
		if err != nil {
			return protocol.OCIEvent{}, err
		}
		event.Kind = protocol.OCIEventOutput
		event.OperationID = protocol.OperationID(output.GetOperationId())
		event.Sequence = output.GetSequence()
		event.Source = source
		event.Stream = outputStream
		event.Data = append([]byte(nil), output.GetData()...)
	case message.GetComplete() != nil:
		complete := message.GetComplete()
		event.Kind = protocol.OCIEventComplete
		event.OperationID = protocol.OperationID(
			complete.GetOperationId(),
		)
		event.Sequence = complete.GetSequence()
		event.Cached = complete.GetCached()
	case message.GetFailed() != nil:
		failed := message.GetFailed()
		event.Kind = protocol.OCIEventFailed
		event.OperationID = protocol.OperationID(failed.GetOperationId())
		event.Sequence = failed.GetSequence()
		event.Message = failed.GetMessage()
	default:
		return protocol.OCIEvent{}, errors.New(
			"agent OCI stream returned an empty event",
		)
	}

	if err := event.Validate(); err != nil {
		return protocol.OCIEvent{}, fmt.Errorf(
			"agent OCI stream returned an invalid event: %w",
			err,
		)
	}

	return event, nil
}

// Close stops following the OCI operation without canceling shared preparation.
func (s *OCIEventStream) Close() error {
	if s == nil || s.cancel == nil {
		return nil
	}

	s.cancel()
	return nil
}
