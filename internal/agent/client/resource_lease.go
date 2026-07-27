package client

// Releases one independently held agent resource lease.

import (
	"context"
	"sync"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

// ResourceLease is the client-side handle for one agent resource.
type ResourceLease struct {
	session    *AgentSession
	resourceID protocol.ResourceID
	leaseID    protocol.LeaseID

	releaseOnce sync.Once
	releaseDone chan struct{}
	releaseErr  error
}

// ResourceID returns the opaque resource identity.
func (l *ResourceLease) ResourceID() protocol.ResourceID {
	if l == nil {
		return ""
	}

	return l.resourceID
}

// LeaseID returns the opaque lease authority.
func (l *ResourceLease) LeaseID() protocol.LeaseID {
	if l == nil {
		return ""
	}

	return l.leaseID
}

// Release releases this one lease.
func (l *ResourceLease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l.releaseOnce.Do(func() {
		l.releaseDone = make(chan struct{})
		go l.finishRelease()
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.releaseDone:
		return l.releaseErr
	}
}

func (l *ResourceLease) finishRelease() {
	defer close(l.releaseDone)

	id, err := protocol.NewCorrelationID()
	if err != nil {
		l.releaseErr = err
		return
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		l.session.options.ReleaseTimeout,
	)
	defer cancel()

	response, err := l.session.client.ReleaseResource(
		ctx,
		&agentv1.ResourceReleaseRequest{
			CorrelationId: string(id),
			SessionId:     string(l.session.sessionID),
			LeaseId:       string(l.leaseID),
		},
	)
	if err != nil {
		l.releaseErr = remoteRequestError(err, id)
		return
	}
	l.releaseErr = requireCorrelation(response.GetCorrelationId(), id)
}
