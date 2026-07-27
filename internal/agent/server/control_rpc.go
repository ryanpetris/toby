package server

// Implements agent status and shutdown RPCs.

import (
	"context"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// Status returns non-secret agent process and resource activity.
func (s *Service) Status(
	_ context.Context,
	request *agentv1.StatusRequest,
) (*agentv1.StatusResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "status request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return nil, err
	}
	defer session.finish(correlationID)

	resources := s.resourceCoordinator.ResourceSnapshot()

	return &agentv1.StatusResponse{
		CorrelationId:   request.GetCorrelationId(),
		BinaryVersion:   s.version,
		State:           protocol.ServiceStateToAgent(s.serviceState()),
		ActiveSessions:  s.sessionCount(),
		ActiveLeases:    resources.ActiveLeases,
		ActiveResources: resources.ActiveResources,
		ActiveStreams:   s.streamCount(),
	}, nil
}

// Stop acknowledges one session request and begins agent teardown.
func (s *Service) Stop(
	_ context.Context,
	request *agentv1.StopRequest,
) (*agentv1.StopResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "stop request is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return nil, err
	}
	defer session.finish(correlationID)

	response := &agentv1.StopResponse{
		CorrelationId: request.GetCorrelationId(),
	}
	go s.gracefulStop()

	return response, nil
}
