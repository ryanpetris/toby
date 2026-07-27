package clientresource

// Implements one resource-kind translation registry without retaining raw
// resource configuration.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/uuid"
)

// Registry owns the per-launch client UUID mappings for one resource kind.
type Registry struct {
	kind    protocol.ResourceKind
	session *agentclient.AgentSession
	logger  *diagnostic.Logger

	mu       sync.Mutex
	bindings map[protocol.ClientResourceID]*binding
	closing  bool
}

type binding struct {
	lease *agentclient.ResourceLease
}

// NewRegistry constructs one launch-scoped resource translation registry.
func NewRegistry(
	kind protocol.ResourceKind,
	session *agentclient.AgentSession,
	logger *diagnostic.Logger,
) (*Registry, error) {
	if session == nil {
		return nil, fmt.Errorf("agent session is required")
	}
	if err := kind.Validate(); err != nil {
		return nil, err
	}

	registry := &Registry{
		kind:     kind,
		session:  session,
		logger:   logger,
		bindings: make(map[protocol.ClientResourceID]*binding),
	}
	go registry.invalidateOnSessionClose()

	return registry, nil
}

// Acquire registers one effective resource configuration with the agent and
// returns only its launch-scoped sandbox-visible UUID.
func (s *Registry) Acquire(
	ctx context.Context,
	effectiveConfiguration any,
) (protocol.ClientResourceID, error) {
	if s == nil || s.session == nil {
		return "", fmt.Errorf("client resource registry is not configured")
	}
	if ctx == nil {
		return "", fmt.Errorf("client resource context is nil")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return "", fmt.Errorf("client resource registry is closing")
	}

	raw, err := json.Marshal(effectiveConfiguration)
	if err != nil {
		return "", fmt.Errorf("encode resource configuration: %w", err)
	}
	clientValue, err := uuid.NewV4()
	if err != nil {
		clear(raw)
		return "", fmt.Errorf("generate client resource ID: %w", err)
	}
	clientID := protocol.ClientResourceID(clientValue)

	lease, err := s.session.Acquire(ctx, s.kind, raw)
	clear(raw)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	select {
	case <-s.session.Done():
		s.mu.Unlock()
		err := fmt.Errorf(
			"agent session closed during acquisition",
		)
		s.logger.DebugError(
			"release resource acquired by a closed agent session",
			lease.Release(context.Background()),
		)
		return "", err
	default:
	}
	if s.closing {
		s.mu.Unlock()
		err := fmt.Errorf("client resource registry is closing")
		s.logger.DebugError(
			"release resource acquired by a closing client registry",
			lease.Release(context.Background()),
		)
		return "", err
	}
	if _, duplicate := s.bindings[clientID]; duplicate {
		s.mu.Unlock()
		err := fmt.Errorf("generated duplicate client resource ID")
		s.logger.DebugError(
			"release resource with a duplicate client ID",
			lease.Release(context.Background()),
		)
		return "", err
	}
	s.bindings[clientID] = &binding{lease: lease}
	s.mu.Unlock()

	return clientID, nil
}

// Open translates one active sandbox-visible UUID and opens its agent data
// stream using the matching opaque resource and lease identities.
func (s *Registry) Open(
	ctx context.Context,
	clientID protocol.ClientResourceID,
) (net.Conn, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("client resource registry is not configured")
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, fmt.Errorf("client resource registry is closing")
	}
	current := s.bindings[clientID]
	s.mu.Unlock()
	if current == nil {
		return nil, fmt.Errorf(
			"client resource %q is not active",
			clientID,
		)
	}

	return s.session.OpenResourceStream(ctx, s.kind, current.lease)
}

// PrepareOCI translates one active sandbox-visible UUID and starts or joins
// its agent-owned preparation operation.
func (s *Registry) PrepareOCI(
	ctx context.Context,
	clientID protocol.ClientResourceID,
) (*agentclient.OCIEventStream, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("client resource registry is not configured")
	}
	if s.kind != protocol.ResourceOCI {
		return nil, fmt.Errorf(
			"client resource registry kind %q does not support OCI preparation",
			s.kind,
		)
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, fmt.Errorf("client resource registry is closing")
	}
	current := s.bindings[clientID]
	s.mu.Unlock()
	if current == nil {
		return nil, fmt.Errorf(
			"client resource %q is not active",
			clientID,
		)
	}

	return s.session.PrepareOCI(ctx, current.lease)
}

// ListModels translates one active models UUID and returns the agent's
// discovered tool-facing metadata.
func (s *Registry) ListModels(
	ctx context.Context,
	clientID protocol.ClientResourceID,
) ([]protocol.ModelsListItemResponse, error) {
	if s == nil || s.session == nil {
		return nil, fmt.Errorf("client resource registry is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("models list context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.kind != protocol.ResourceModels {
		return nil, fmt.Errorf(
			"client resource registry kind %q does not support model listing",
			s.kind,
		)
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, fmt.Errorf("client resource registry is closing")
	}
	current := s.bindings[clientID]
	s.mu.Unlock()
	if current == nil {
		return nil, fmt.Errorf(
			"client resource %q is not active",
			clientID,
		)
	}

	return s.session.ListModels(ctx, current.lease)
}

// Close releases every mapping independently and returns all release errors.
func (s *Registry) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	leases := make([]*agentclient.ResourceLease, 0, len(s.bindings))
	for _, current := range s.bindings {
		leases = append(leases, current.lease)
	}
	clear(s.bindings)
	s.mu.Unlock()

	for _, lease := range leases {
		s.logger.DebugError(
			"release resource while closing client registry",
			lease.Release(ctx),
			"resource_kind", s.kind,
		)
	}

	return nil
}

func (s *Registry) invalidateOnSessionClose() {
	<-s.session.Done()

	s.mu.Lock()
	s.closing = true
	clear(s.bindings)
	s.mu.Unlock()
}
