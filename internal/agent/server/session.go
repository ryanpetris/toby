package server

// Owns one client-opened agent stream and every lease acquired through it.

import (
	"context"
	"fmt"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

type agentSession struct {
	server *Service
	id     protocol.SessionID
	stream agentv1.AgentService_OpenSessionServer
	caller *hostActionCaller

	ctx    context.Context
	cancel context.CancelFunc

	sendMu sync.Mutex
	mu     sync.Mutex

	correlations map[protocol.CorrelationID]struct{}
	leases       map[protocol.LeaseID]ResourceLease
	released     map[protocol.LeaseID]struct{}
	stoppingID   protocol.CorrelationID
	stoppingDone chan struct{}
	stoppingOnce sync.Once
	closed       bool
}

func (s *agentSession) receive(
	message *agentv1.SessionClientMessage,
) error {
	if message == nil {
		return invalidRequest("", "session message is required")
	}

	correlationID := protocol.CorrelationID(message.GetCorrelationId())
	if err := protocol.ValidateCorrelationID(correlationID); err != nil {
		return invalidRequest(
			message.GetCorrelationId(),
			"session message correlation ID is invalid",
		)
	}

	switch {
	case message.GetHostActionResponse() != nil:
		return s.caller.deliverResponse(
			correlationID,
			message.GetHostActionResponse().GetPayload(),
		)
	case message.GetHostActionError() != nil:
		detail := message.GetHostActionError()
		code, err := protocol.ErrorCodeFromAgent(detail.GetCode())
		if err != nil {
			return invalidRequest(
				message.GetCorrelationId(),
				"host action error code is invalid",
			)
		}
		if err := validateHostActionMessage(detail.GetMessage()); err != nil {
			return invalidRequest(
				message.GetCorrelationId(),
				"host action error message is invalid",
			)
		}
		return s.caller.deliverError(
			correlationID,
			HostActionError{
				Code:      code,
				Message:   detail.GetMessage(),
				Retryable: detail.GetRetryable(),
			},
		)
	case message.GetShutdownResponse() != nil:
		return s.acknowledgeStopping(correlationID)
	default:
		return invalidRequest(
			message.GetCorrelationId(),
			"message is not valid after session open",
		)
	}
}

func (s *agentSession) requestStopping(
	grace time.Duration,
) (<-chan struct{}, error) {
	if grace <= 0 {
		return nil, fmt.Errorf("client shutdown grace must be positive")
	}
	id, err := protocol.NewCorrelationID()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		closed := make(chan struct{})
		close(closed)
		return closed, nil
	}
	if s.stoppingID != "" {
		done := s.stoppingDone
		s.mu.Unlock()
		return done, nil
	}
	s.stoppingID = id
	s.stoppingDone = make(chan struct{})
	done := s.stoppingDone
	s.mu.Unlock()

	err = s.send(&agentv1.SessionServerMessage{
		CorrelationId: string(id),
		Value: &agentv1.SessionServerMessage_ShutdownRequest{
			ShutdownRequest: &agentv1.ShutdownRequest{
				GracePeriodMilliseconds: uint64(grace / time.Millisecond),
			},
		},
	})
	if err != nil {
		s.closeStopping()
		return done, err
	}
	return done, nil
}

func (s *agentSession) acknowledgeStopping(
	id protocol.CorrelationID,
) error {
	s.mu.Lock()
	expected := s.stoppingID
	s.mu.Unlock()
	if expected == "" || id != expected {
		return invalidRequest(
			string(id),
			"agent shutdown response correlation ID is not active",
		)
	}

	s.closeStopping()
	return nil
}

func (s *agentSession) closeStopping() {
	s.stoppingOnce.Do(func() {
		s.mu.Lock()
		done := s.stoppingDone
		s.mu.Unlock()
		if done != nil {
			close(done)
		}
	})
}

func (s *agentSession) send(
	message *agentv1.SessionServerMessage,
) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	if err := s.ctx.Err(); err != nil {
		return err
	}

	return s.stream.Send(message)
}

func (s *agentSession) begin(id protocol.CorrelationID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}
	if _, exists := s.correlations[id]; exists {
		return false
	}
	s.correlations[id] = struct{}{}

	return true
}

func (s *agentSession) finish(id protocol.CorrelationID) {
	s.mu.Lock()
	delete(s.correlations, id)
	s.mu.Unlock()
}

func (s *agentSession) addLease(lease ResourceLease) bool {
	if lease == nil {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}
	leaseID := lease.LeaseID()
	if _, exists := s.leases[leaseID]; exists {
		return false
	}
	s.leases[leaseID] = lease

	return true
}

func (s *agentSession) takeLease(
	leaseID protocol.LeaseID,
) (ResourceLease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	lease := s.leases[leaseID]
	_, released := s.released[leaseID]
	if lease != nil {
		delete(s.leases, leaseID)
		s.released[leaseID] = struct{}{}
	}

	return lease, released
}

func (s *agentSession) ownsLease(
	resourceID protocol.ResourceID,
	leaseID protocol.LeaseID,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	lease := s.leases[leaseID]
	return !s.closed &&
		lease != nil &&
		lease.ResourceID() == resourceID
}

func (s *agentSession) ownsLeaseID(leaseID protocol.LeaseID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return !s.closed && s.leases[leaseID] != nil
}

func (s *agentSession) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.cancel()
	leases := make([]ResourceLease, 0, len(s.leases))
	for _, lease := range s.leases {
		leases = append(leases, lease)
	}
	clear(s.leases)
	clear(s.correlations)
	s.mu.Unlock()
	s.closeStopping()

	s.caller.revoke()
	releaseCtx, cancel := context.WithTimeout(
		context.Background(),
		min(
			s.server.options.ReleaseTimeout,
			s.server.options.TransportShutdownGrace,
		),
	)
	defer cancel()
	var releases sync.WaitGroup
	for _, lease := range leases {
		releases.Add(1)
		go func(current ResourceLease) {
			defer releases.Done()
			s.releaseDetached(releaseCtx, current)
		}(lease)
	}
	done := make(chan struct{})
	go func() {
		releases.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-releaseCtx.Done():
	}
}

func (s *agentSession) releaseDetached(
	ctx context.Context,
	lease ResourceLease,
) {
	if lease == nil {
		return
	}

	if err := lease.Release(ctx); err != nil {
		s.server.options.Logger.DebugError(
			"release disconnected agent-session lease",
			err,
			"lease_id",
			lease.LeaseID(),
			"resource_id",
			lease.ResourceID(),
		)
	}
}

func validateHostActionMessage(message string) error {
	if message == "" {
		return fmt.Errorf("host action error message is required")
	}
	if len(message) > 4<<10 {
		return fmt.Errorf("host action error message is too large")
	}

	return nil
}
