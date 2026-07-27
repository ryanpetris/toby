package server

// Resolves session-scoped correlation and lease authority for agent RPCs.

import (
	"petris.dev/toby/internal/agent/protocol"
)

func (s *Service) requestSession(
	sessionValue string,
	correlationValue string,
) (*agentSession, protocol.CorrelationID, error) {
	correlationID := protocol.CorrelationID(correlationValue)
	if err := protocol.ValidateCorrelationID(correlationID); err != nil {
		return nil, "", invalidRequest(
			correlationValue,
			"correlation ID is invalid",
		)
	}

	sessionID := protocol.SessionID(sessionValue)
	if err := protocol.ValidateSessionID(sessionID); err != nil {
		return nil, correlationID, invalidRequest(
			correlationValue,
			"session ID is invalid",
		)
	}
	session := s.session(sessionID)
	if session == nil {
		return nil, correlationID, agentError(
			correlationValue,
			protocol.ErrorUnavailable,
			"agent session is unavailable",
			false,
		)
	}
	if !session.begin(correlationID) {
		return nil, correlationID, invalidRequest(
			correlationValue,
			"correlation ID is already active in this session",
		)
	}

	return session, correlationID, nil
}

func requireLease(
	session *agentSession,
	resourceValue string,
	leaseValue string,
	correlationID string,
) (protocol.ResourceID, protocol.LeaseID, error) {
	resourceID := protocol.ResourceID(resourceValue)
	if err := protocol.ValidateResourceID(resourceID); err != nil {
		return "", "", invalidRequest(
			correlationID,
			"resource ID is invalid",
		)
	}
	leaseID := protocol.LeaseID(leaseValue)
	if err := protocol.ValidateLeaseID(leaseID); err != nil {
		return "", "", invalidRequest(
			correlationID,
			"lease ID is invalid",
		)
	}
	if !session.ownsLease(resourceID, leaseID) {
		return "", "", agentError(
			correlationID,
			protocol.ErrorLeaseNotFound,
			"resource is not held by this session lease",
			false,
		)
	}

	return resourceID, leaseID, nil
}
