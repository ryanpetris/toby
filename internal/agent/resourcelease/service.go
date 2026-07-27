package resourcelease

// Coordinates stable resource entries and independent immediate leases.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/resourcehash"
	"petris.dev/toby/internal/uuid"
)

// ListModels discovers models for one active lease without exposing resource
// configuration.
func (s *Service) ListModels(
	ctx context.Context,
	leaseID protocol.LeaseID,
) (map[string]any, error) {
	resolved, caller, err := s.lookupLease(leaseID)
	if err != nil {
		return nil, err
	}
	if resolved.Kind != protocol.ResourceModels {
		return nil, fmt.Errorf("lease does not identify a models resource")
	}
	operator, ok := s.openers[protocol.ResourceModels].(ModelsOperator)
	if !ok {
		return nil, fmt.Errorf("models operations are unavailable")
	}

	return operator.ListModels(ctx, StreamRequest{
		Resource: resolved,
		Caller:   caller,
	})
}

// FlushModelsCache invalidates one active models resource cache.
func (s *Service) FlushModelsCache(
	leaseID protocol.LeaseID,
) error {
	resolved, _, err := s.lookupLease(leaseID)
	if err != nil {
		return err
	}
	if resolved.Kind != protocol.ResourceModels {
		return fmt.Errorf("lease does not identify a models resource")
	}
	operator, ok := s.openers[protocol.ResourceModels].(ModelsOperator)
	if !ok {
		return fmt.Errorf("models operations are unavailable")
	}
	operator.FlushModelsCache(resolved)

	return nil
}

// ResourceItems returns sorted non-secret active resource entries.
func (s *Service) ResourceItems() []server.ResourceItem {
	if s == nil {
		return nil
	}

	byID, _ := s.snapshotItems()
	items := make([]server.ResourceItem, 0, len(byID))
	for _, item := range byID {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ID < items[j].ID
	})

	return items
}

func (s *Service) snapshotItems() (
	map[protocol.ResourceID]server.ResourceItem,
	uint64,
) {
	s.mu.Lock()
	byID := make(
		map[protocol.ResourceID]server.ResourceItem,
		len(s.entriesByID),
	)
	for _, current := range s.entriesByID {
		byID[current.resolved.ID] = server.ResourceItem{
			ID:           current.resolved.ID,
			Kind:         current.resolved.Kind,
			ActiveLeases: uint64(len(current.leases)),
		}
	}
	activeLeases := uint64(len(s.leases))
	s.mu.Unlock()

	for kind, opener := range s.openers {
		lister, ok := opener.(RuntimeLister)
		if !ok {
			continue
		}
		for _, id := range lister.RuntimeResourceIDs() {
			if id == "" {
				continue
			}
			if _, registered := byID[id]; registered {
				continue
			}
			byID[id] = server.ResourceItem{
				ID:   id,
				Kind: kind,
			}
		}
	}

	return byID, activeLeases
}

// Service is the agent's registry for effective resource configurations and
// their independently releasable leases.
type Service struct {
	resolvers map[protocol.ResourceKind]Resolver
	openers   map[protocol.ResourceKind]ResourceOpener

	mu              sync.Mutex
	entriesByDigest map[resourcehash.Digest]*entry
	entriesByID     map[protocol.ResourceID]*entry
	digestsByID     map[protocol.ResourceID]resourcehash.Digest
	leases          map[protocol.LeaseID]*Lease
	closing         bool
}

type entry struct {
	resolved Resolved
	leases   map[protocol.LeaseID]*Lease
}

var _ server.ResourceCoordinator = (*Service)(nil)

// NewService validates the closed resource resolver and opener registries.
func NewService(
	resolvers []Resolver,
	openers []ResourceOpener,
) (*Service, error) {
	byKind := make(
		map[protocol.ResourceKind]Resolver,
		len(resolvers),
	)
	for index, resolver := range resolvers {
		if resolver == nil {
			return nil, fmt.Errorf("resource resolver %d is nil", index)
		}
		kind := resolver.Kind()
		if err := kind.Validate(); err != nil {
			return nil, fmt.Errorf(
				"resource resolver %d: %w",
				index,
				err,
			)
		}
		if _, duplicate := byKind[kind]; duplicate {
			return nil, fmt.Errorf(
				"resource resolver kind %q is registered more than once",
				kind,
			)
		}
		byKind[kind] = resolver
	}

	byOpenerKind := make(
		map[protocol.ResourceKind]ResourceOpener,
		len(openers),
	)
	for index, opener := range openers {
		if opener == nil {
			return nil, fmt.Errorf("resource opener %d is nil", index)
		}
		kind := opener.Kind()
		if err := kind.Validate(); err != nil {
			return nil, fmt.Errorf(
				"resource opener %d: %w",
				index,
				err,
			)
		}
		if _, duplicate := byOpenerKind[kind]; duplicate {
			return nil, fmt.Errorf(
				"resource opener kind %q is registered more than once",
				kind,
			)
		}
		byOpenerKind[kind] = opener
	}

	return &Service{
		resolvers:       byKind,
		openers:         byOpenerKind,
		entriesByDigest: make(map[resourcehash.Digest]*entry),
		entriesByID:     make(map[protocol.ResourceID]*entry),
		digestsByID:     make(map[protocol.ResourceID]resourcehash.Digest),
		leases:          make(map[protocol.LeaseID]*Lease),
	}, nil
}

// OpenResource validates an active resource/lease pair and asks the matching
// resource-specific opener for one operation.
func (s *Service) OpenResource(
	ctx context.Context,
	resourceKind protocol.ResourceKind,
	resourceID protocol.ResourceID,
	leaseID protocol.LeaseID,
) (server.ResourceStream, error) {
	if ctx == nil {
		return nil, fmt.Errorf("resource stream context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if err := resourceKind.Validate(); err != nil {
		return nil, err
	}
	resolved, caller, leaseLifetime, err := s.lookupStream(
		resourceID,
		leaseID,
	)
	if err != nil {
		return nil, err
	}
	if resolved.Kind != resourceKind {
		return nil, fmt.Errorf(
			"requested resource kind %q does not match resource kind %q",
			resourceKind,
			resolved.Kind,
		)
	}
	opener := s.openers[resourceKind]
	if opener == nil {
		return nil, fmt.Errorf(
			"resource kind %q has no opener",
			resourceKind,
		)
	}
	openCtx, cancel := context.WithCancel(ctx)
	stopLeaseCancellation := context.AfterFunc(
		leaseLifetime,
		cancel,
	)
	defer func() {
		stopLeaseCancellation()
		cancel()
	}()

	stream, err := opener.Open(openCtx, StreamRequest{
		Resource: resolved,
		Caller:   caller,
	})
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, fmt.Errorf(
			"resource opener for %q returned nil",
			resourceKind,
		)
	}
	if err := openCtx.Err(); err != nil {
		diagnostic.DiscardError(
			"the resource stream open was cancelled",
			"close resource stream after open cancellation",
			stream.Close(),
			"resource_kind", resourceKind,
			"resource_id", resourceID,
			"lease_id", leaseID,
		)
		return nil, err
	}

	return stream, nil
}

// AcquireResource resolves one configuration and records an immediate lease
// without activating its runtime.
func (s *Service) AcquireResource(
	ctx context.Context,
	request protocol.ResourceAcquireRequest,
	caller server.HostActionCaller,
) (server.ResourceLease, error) {
	if s == nil {
		return nil, fmt.Errorf("resource lease service is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("resource acquisition context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	resolver := s.resolvers[request.Kind]
	if resolver == nil {
		return nil, fmt.Errorf(
			"resource kind %q is not configured",
			request.Kind,
		)
	}
	resolved, err := resolver.Resolve(
		ctx,
		append([]byte(nil), request.Configuration...),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"resolve %s configuration: %w",
			request.Kind,
			err,
		)
	}
	if err := validateResolved(request.Kind, resolved); err != nil {
		return nil, err
	}

	leaseValue, err := uuid.NewV4()
	if err != nil {
		return nil, fmt.Errorf("generate resource lease ID: %w", err)
	}
	leaseID := protocol.LeaseID(leaseValue)

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, fmt.Errorf("resource lease service is shutting down")
	}
	if _, duplicate := s.leases[leaseID]; duplicate {
		s.mu.Unlock()
		return nil, fmt.Errorf("generated duplicate resource lease ID")
	}

	current := s.entriesByDigest[resolved.Digest]
	if current == nil {
		knownDigest, knownID := s.digestsByID[resolved.ID]
		if knownID && knownDigest != resolved.Digest {
			s.mu.Unlock()
			return nil, fmt.Errorf(
				"resource UUID collision for %s",
				resolved.ID,
			)
		}
		current = &entry{
			resolved: resolved,
			leases:   make(map[protocol.LeaseID]*Lease),
		}
		s.entriesByDigest[resolved.Digest] = current
		s.entriesByID[resolved.ID] = current
		s.digestsByID[resolved.ID] = resolved.Digest
	} else if current.resolved.ID != resolved.ID ||
		current.resolved.Kind != resolved.Kind ||
		!sameConfiguration(
			current.resolved.Configuration,
			resolved.Configuration,
		) {
		s.mu.Unlock()
		return nil, fmt.Errorf(
			"resource identity collision for %s",
			resolved.ID,
		)
	}

	leaseLifetime, cancelLease := context.WithCancel(context.Background())
	lease := &Lease{
		service:    s,
		resourceID: resolved.ID,
		leaseID:    leaseID,
		caller:     caller,
		lifetime:   leaseLifetime,
		cancel:     cancelLease,
	}
	current.leases[leaseID] = lease
	s.leases[leaseID] = lease
	s.mu.Unlock()

	s.notifyLeaseAcquired(resolved)

	return lease, nil
}

func sameConfiguration(left, right any) bool {
	leftDocument, leftErr := json.Marshal(left)
	if leftErr != nil {
		return false
	}
	defer clear(leftDocument)

	rightDocument, rightErr := json.Marshal(right)
	if rightErr != nil {
		return false
	}
	defer clear(rightDocument)

	return bytes.Equal(leftDocument, rightDocument)
}

func (s *Service) lookupStream(
	resourceID protocol.ResourceID,
	leaseID protocol.LeaseID,
) (
	Resolved,
	server.HostActionCaller,
	context.Context,
	error,
) {
	if s == nil {
		return Resolved{}, nil, nil, fmt.Errorf(
			"resource lease service is nil",
		)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lease := s.leases[leaseID]
	if lease == nil || lease.resourceID != resourceID || lease.closed {
		return Resolved{}, nil, nil, fmt.Errorf(
			"resource lease is unavailable",
		)
	}
	current := s.entriesByID[resourceID]
	if current == nil {
		return Resolved{}, nil, nil, fmt.Errorf(
			"resource is unavailable",
		)
	}

	return current.resolved, lease.caller, lease.lifetime, nil
}

func (s *Service) lookupLease(
	leaseID protocol.LeaseID,
) (Resolved, server.HostActionCaller, error) {
	if s == nil {
		return Resolved{}, nil, fmt.Errorf(
			"resource lease service is nil",
		)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	lease := s.leases[leaseID]
	if lease == nil || lease.closed {
		return Resolved{}, nil, fmt.Errorf("resource lease is unavailable")
	}
	current := s.entriesByID[lease.resourceID]
	if current == nil {
		return Resolved{}, nil, fmt.Errorf("resource is unavailable")
	}

	return current.resolved, lease.caller, nil
}

// Snapshot returns resource and lease counts.
func (s *Service) Snapshot() Snapshot {
	if s == nil {
		return Snapshot{}
	}

	items, activeLeases := s.snapshotItems()

	return Snapshot{
		ActiveResources: uint64(len(items)),
		ActiveLeases:    activeLeases,
	}
}

// ResourceSnapshot returns the server protocol's non-secret activity counts.
func (s *Service) ResourceSnapshot() server.ResourceSnapshot {
	snapshot := s.Snapshot()

	return server.ResourceSnapshot{
		ActiveResources: snapshot.ActiveResources,
		ActiveLeases:    snapshot.ActiveLeases,
	}
}

// Shutdown refuses future acquisition and releases all registry references.
func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	for _, lease := range s.leases {
		lease.closed = true
		lease.caller = nil
		lease.cancel()
	}
	clear(s.leases)
	clear(s.entriesByDigest)
	clear(s.entriesByID)
	clear(s.digestsByID)
	s.mu.Unlock()

	var lifecycles sync.WaitGroup
	results := make(chan error, len(s.openers))
	for _, opener := range s.openers {
		if lifecycle, ok := opener.(RuntimeLifecycle); ok {
			lifecycles.Add(1)
			go func(current RuntimeLifecycle) {
				defer lifecycles.Done()
				results <- current.Shutdown(ctx)
			}(lifecycle)
		}
	}
	done := make(chan struct{})
	go func() {
		lifecycles.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	close(results)

	var shutdownErr error
	for err := range results {
		shutdownErr = errors.Join(shutdownErr, err)
	}
	return shutdownErr
}

func (s *Service) release(lease *Lease) {
	s.mu.Lock()

	if lease.closed {
		s.mu.Unlock()
		return
	}
	lease.closed = true
	lease.caller = nil
	lease.cancel()
	delete(s.leases, lease.leaseID)

	current := s.entriesByID[lease.resourceID]
	var resolved Resolved
	if current != nil {
		resolved = current.resolved
		delete(current.leases, lease.leaseID)
		if len(current.leases) == 0 {
			delete(s.entriesByDigest, current.resolved.Digest)
			delete(s.entriesByID, lease.resourceID)
		}
	}
	s.mu.Unlock()

	if resolved.Configuration != nil {
		s.notifyLeaseReleased(resolved)
	}
}

func validateResolved(
	requested protocol.ResourceKind,
	resolved Resolved,
) error {
	if resolved.Kind != requested {
		return fmt.Errorf(
			"resource resolver for %q returned kind %q",
			requested,
			resolved.Kind,
		)
	}
	if resolved.ID == "" {
		return fmt.Errorf(
			"resource resolver for %q returned an empty identity",
			requested,
		)
	}
	if resolved.Digest.IsZero() {
		return fmt.Errorf(
			"resource resolver for %q returned an empty digest",
			requested,
		)
	}
	if resolved.Configuration == nil {
		return fmt.Errorf(
			"resource resolver for %q returned nil configuration",
			requested,
		)
	}

	return nil
}

func (s *Service) notifyLeaseAcquired(resolved Resolved) {
	opener := s.openers[resolved.Kind]
	if lifecycle, ok := opener.(RuntimeLifecycle); ok {
		lifecycle.LeaseAcquired(resolved)
	}
}

func (s *Service) notifyLeaseReleased(resolved Resolved) {
	opener := s.openers[resolved.Kind]
	if lifecycle, ok := opener.(RuntimeLifecycle); ok {
		lifecycle.LeaseReleased(resolved)
	}
}
