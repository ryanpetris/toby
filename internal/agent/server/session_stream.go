package server

// Opens and receives one client-owned agent session stream.

import (
	"context"
	"errors"
	"io"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
	"petris.dev/toby/internal/uuid"
)

// OpenSession opens the one client-initiated stream used for reverse host
// actions and as the lifetime authority for resource leases.
func (s *Service) OpenSession(
	stream agentv1.AgentService_OpenSessionServer,
) error {
	if stream == nil {
		return invalidRequest("", "session stream is required")
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	correlationID := protocol.CorrelationID(first.GetCorrelationId())
	if err := protocol.ValidateCorrelationID(correlationID); err != nil {
		return invalidRequest(
			first.GetCorrelationId(),
			"session-open correlation ID is invalid",
		)
	}
	if first.GetOpen() == nil {
		return invalidRequest(
			first.GetCorrelationId(),
			"session open must be the first stream message",
		)
	}

	value, err := uuid.NewV4()
	if err != nil {
		return agentError(
			first.GetCorrelationId(),
			protocol.ErrorInternal,
			"create agent session",
			false,
		)
	}

	sessionCtx, cancel := context.WithCancel(stream.Context())
	session := &agentSession{
		server:       s,
		id:           protocol.SessionID(value),
		stream:       stream,
		ctx:          sessionCtx,
		cancel:       cancel,
		correlations: make(map[protocol.CorrelationID]struct{}),
		leases:       make(map[protocol.LeaseID]ResourceLease),
		released:     make(map[protocol.LeaseID]struct{}),
	}
	session.caller = newHostActionCaller(session, s.options)

	if err := s.registerSession(session); err != nil {
		cancel()
		return agentError(
			first.GetCorrelationId(),
			protocol.ErrorUnavailable,
			"agent is not accepting sessions",
			true,
		)
	}
	defer s.unregisterSession(session)
	defer session.close()

	if err := session.caller.activate(); err != nil {
		return err
	}
	if err := session.send(&agentv1.SessionServerMessage{
		CorrelationId: first.GetCorrelationId(),
		Value: &agentv1.SessionServerMessage_Opened{
			Opened: &agentv1.SessionOpenResponse{
				SessionId: string(session.id),
				TransportCapabilities: []agentv1.TransportCapability{
					protocol.TransportCapabilityToAgent(
						protocol.TransportUnixSocket,
					),
				},
			},
		},
	}); err != nil {
		return err
	}

	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) || session.ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return err
		}
		if err := session.receive(message); err != nil {
			return err
		}
	}
}
