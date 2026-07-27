package providergateway

// Owns one run's provider routes, immediate revocation, descriptor, and
// confirmed physical Caddy removal.

import (
	"context"
	"fmt"
	"sync"
)

type acquired struct {
	gateway    *Gateway
	routeIDs   []string
	descriptor DescriptorConfig

	revokeOnce  sync.Once
	releaseOnce sync.Once
	releaseDone chan struct{}

	mu              sync.Mutex
	revoked         bool
	removalRevision uint64
	releaseErr      error
}

func (a *acquired) Revoke() {
	if a == nil {
		return
	}

	a.revokeOnce.Do(func() {
		a.mu.Lock()
		a.revoked = true
		a.mu.Unlock()

		revision := a.gateway.revoke(a.routeIDs)

		a.mu.Lock()
		a.removalRevision = revision
		a.mu.Unlock()
	})
}

func (a *acquired) Release(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf(
			"models gateway release context is nil",
		)
	}

	a.Revoke()
	a.releaseOnce.Do(func() {
		go a.finishRelease()
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.releaseDone:
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.releaseErr
	}
}

func (a *acquired) finishRelease() {
	defer close(a.releaseDone)
	defer a.gateway.unregister(a)

	a.mu.Lock()
	revision := a.removalRevision
	a.mu.Unlock()

	ctx, cancel := context.WithTimeout(
		context.Background(),
		a.gateway.options.CleanupTimeout,
	)
	defer cancel()

	var err error
	if !a.gateway.isClosing() {
		err = a.gateway.waitRemoval(ctx, revision)
	}

	a.mu.Lock()
	a.releaseErr = err
	a.mu.Unlock()
}
