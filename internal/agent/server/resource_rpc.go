package server

// Implements session-scoped resource lease RPCs.

import (
	"context"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// AcquireResource registers one configuration and returns its opaque resource
// and lease identities without starting the resource runtime.
func (s *Service) AcquireResource(
	ctx context.Context,
	request *agentv1.ResourceAcquireRequest,
) (*agentv1.ResourceAcquireResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "resource acquisition is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return nil, err
	}
	defer session.finish(correlationID)

	kind, err := protocol.ResourceKindFromAgent(request.GetKind())
	if err != nil {
		return nil, invalidRequest(
			request.GetCorrelationId(),
			"resource kind is invalid",
		)
	}
	configuration := append([]byte(nil), request.GetConfiguration()...)
	if err := protocol.ValidateConfigurationDocument(configuration); err != nil {
		clear(configuration)
		return nil, invalidRequest(
			request.GetCorrelationId(),
			"resource configuration is invalid",
		)
	}
	defer clear(configuration)

	acquireCtx, cancel := boundedServerContext(
		ctx,
		s.options.AcquireTimeout,
	)
	lease, err := s.resourceCoordinator.AcquireResource(
		acquireCtx,
		protocol.ResourceAcquireRequest{
			CorrelationID: correlationID,
			Kind:          kind,
			Configuration: configuration,
		},
		session.caller,
	)
	cancel()
	if err != nil {
		return nil, agentError(
			request.GetCorrelationId(),
			protocol.ErrorAcquireFailed,
			"resource acquisition failed",
			true,
		)
	}
	if !session.addLease(lease) {
		releaseCtx, cancel := context.WithTimeout(
			context.Background(),
			s.options.ReleaseTimeout,
		)
		session.releaseDetached(releaseCtx, lease)
		cancel()
		return nil, agentError(
			request.GetCorrelationId(),
			protocol.ErrorInternal,
			"agent could not retain the resource lease",
			false,
		)
	}

	return &agentv1.ResourceAcquireResponse{
		CorrelationId: request.GetCorrelationId(),
		ResourceId:    string(lease.ResourceID()),
		LeaseId:       string(lease.LeaseID()),
	}, nil
}

// ReleaseResource releases one lease owned by the selected agent session.
func (s *Service) ReleaseResource(
	ctx context.Context,
	request *agentv1.ResourceReleaseRequest,
) (*agentv1.ResourceReleaseResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "resource release is required")
	}

	session, correlationID, err := s.requestSession(
		request.GetSessionId(),
		request.GetCorrelationId(),
	)
	if err != nil {
		return nil, err
	}
	defer session.finish(correlationID)

	leaseID := protocol.LeaseID(request.GetLeaseId())
	if err := protocol.ValidateLeaseID(leaseID); err != nil {
		return nil, invalidRequest(
			request.GetCorrelationId(),
			"lease ID is invalid",
		)
	}
	lease, alreadyReleased := session.takeLease(leaseID)
	if lease == nil && !alreadyReleased {
		return nil, agentError(
			request.GetCorrelationId(),
			protocol.ErrorLeaseNotFound,
			"resource lease is not owned by this session",
			false,
		)
	}
	if lease != nil {
		releaseCtx, cancel := boundedServerContext(
			ctx,
			s.options.ReleaseTimeout,
		)
		err := lease.Release(releaseCtx)
		cancel()
		if err != nil {
			return nil, agentError(
				request.GetCorrelationId(),
				protocol.ErrorInternal,
				"resource lease release failed",
				false,
			)
		}
	}

	return &agentv1.ResourceReleaseResponse{
		CorrelationId: request.GetCorrelationId(),
	}, nil
}
