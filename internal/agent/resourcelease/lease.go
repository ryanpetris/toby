package resourcelease

// Implements an immediate, idempotent agent resource lease.

import (
	"context"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/server"
)

// Lease is one client's interest in one agent resource.
type Lease struct {
	service    *Service
	resourceID protocol.ResourceID
	leaseID    protocol.LeaseID
	caller     server.HostActionCaller
	lifetime   context.Context
	cancel     context.CancelFunc

	closed bool
}

var _ server.ResourceLease = (*Lease)(nil)

// ResourceID returns the resource's opaque stable identity.
func (l *Lease) ResourceID() protocol.ResourceID {
	if l == nil {
		return ""
	}

	return l.resourceID
}

// LeaseID returns this acquisition's opaque authority.
func (l *Lease) LeaseID() protocol.LeaseID {
	if l == nil {
		return ""
	}

	return l.leaseID
}

// Release immediately removes this lease from the resource registry.
func (l *Lease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.service.release(l)
	if ctx != nil {
		return ctx.Err()
	}

	return nil
}

// Done closes when the lease is released or the registry shuts down.
func (l *Lease) Done() <-chan struct{} {
	if l == nil || l.lifetime == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return l.lifetime.Done()
}
