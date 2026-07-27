package resourcepool

// Implements run-local admission and generation-bound connector leases over a
// shared agent resource lease.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
	"petris.dev/toby/internal/mcpgateway/localhttp"
)

type serviceLease struct {
	pool        *Pool
	key         resource.Key
	lease       *resource.Lease
	idleTimeout time.Duration

	mu         sync.Mutex
	accepting  bool
	connectors map[*serviceConnector]struct{}

	revokeOnce  sync.Once
	releaseOnce sync.Once
	releaseErr  error
}

var _ localhttp.ServiceLease = (*serviceLease)(nil)

func (l *serviceLease) OpenConnector(
	ctx context.Context,
) (localhttp.ServiceConnector, error) {
	if l == nil || l.lease == nil {
		return nil, fmt.Errorf("local HTTP MCP service lease is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("open local HTTP MCP connector context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	for {
		lease, err := l.activeLease(ctx)
		if err != nil {
			return nil, err
		}

		l.mu.Lock()
		if !l.accepting {
			l.mu.Unlock()
			return nil, resource.ErrLeaseClosed
		}
		if l.lease != lease {
			l.mu.Unlock()
			continue
		}

		generation, err := lease.OpenConnector()
		if err != nil {
			l.mu.Unlock()
			if errors.Is(err, resource.ErrLeaseClosed) ||
				errors.Is(err, resource.ErrResourceUnavailable) {
				continue
			}
			return nil, err
		}
		instance, err := lease.Instance()
		if err != nil {
			l.mu.Unlock()
			generation.Close()
			if errors.Is(err, resource.ErrLeaseClosed) ||
				errors.Is(err, resource.ErrResourceUnavailable) ||
				errors.Is(err, resource.ErrResourceExited) {
				continue
			}
			return nil, err
		}
		httpInstance, ok := instance.(Instance)
		if !ok {
			l.mu.Unlock()
			generation.Close()
			return nil, fmt.Errorf(
				"local HTTP MCP generation has incompatible instance %T",
				instance,
			)
		}
		upstream, err := httpInstance.Upstream()
		if err != nil {
			l.mu.Unlock()
			generation.Close()
			return nil, err
		}

		connector := &serviceConnector{
			owner:      l,
			generation: generation,
			upstream:   cloneUpstream(upstream),
		}
		l.connectors[connector] = struct{}{}
		l.mu.Unlock()

		return connector, nil
	}
}

func (l *serviceLease) activeLease(
	ctx context.Context,
) (*resource.Lease, error) {
	for {
		l.mu.Lock()
		if !l.accepting {
			l.mu.Unlock()
			return nil, resource.ErrLeaseClosed
		}
		current := l.lease
		if current != nil && current.State() == resource.LeaseActive {
			l.mu.Unlock()
			return current, nil
		}
		l.mu.Unlock()

		replacement, err := l.pool.registry.AcquireWithPolicy(
			ctx,
			l.key,
			resource.AcquisitionPolicy{
				IdleTimeout: l.idleTimeout,
			},
		)
		if err != nil {
			var retry *resource.RetryError
			if !errors.As(err, &retry) {
				return nil, err
			}

			delay := time.Until(retry.RetryAt)
			if delay <= 0 {
				continue
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return nil, ctx.Err()
			case <-timer.C:
				continue
			}
		}

		l.mu.Lock()
		if !l.accepting {
			l.mu.Unlock()
			replacement.Release()
			return nil, resource.ErrLeaseClosed
		}
		current = l.lease
		if current != nil && current.State() == resource.LeaseActive {
			l.mu.Unlock()
			replacement.Release()
			return current, nil
		}
		l.lease = replacement
		l.mu.Unlock()

		if current != nil {
			current.Release()
		}
		return replacement, nil
	}
}

func (l *serviceLease) Revoke() {
	if l == nil {
		return
	}

	l.revokeOnce.Do(func() {
		l.mu.Lock()
		l.accepting = false
		connectors := make([]*serviceConnector, 0, len(l.connectors))
		for connector := range l.connectors {
			connectors = append(connectors, connector)
		}
		l.mu.Unlock()

		for _, connector := range connectors {
			connector.Close()
		}
	})
}

func (l *serviceLease) Release(ctx context.Context) error {
	if l == nil {
		return nil
	}

	l.Revoke()
	l.releaseOnce.Do(func() {
		if l.lease != nil {
			l.lease.Release()
		}
		if l.pool != nil {
			// The final run releases the retained exact mount capabilities
			// after the current process generation has already established its
			// mounts.
			l.releaseErr = l.pool.unregister(l.key)
		}
	})

	if ctx == nil {
		return l.releaseErr
	}
	return errors.Join(l.releaseErr, ctx.Err())
}

type serviceConnector struct {
	owner      *serviceLease
	generation *resource.Connector
	upstream   httpbridge.Upstream

	closeOnce sync.Once
}

var _ localhttp.ServiceConnector = (*serviceConnector)(nil)

func (c *serviceConnector) Upstream() (httpbridge.Upstream, error) {
	if c == nil || c.generation == nil {
		return httpbridge.Upstream{}, fmt.Errorf(
			"local HTTP MCP generation connector is not configured",
		)
	}
	select {
	case <-c.generation.Done():
		return httpbridge.Upstream{}, resource.ErrResourceUnavailable
	default:
		return cloneUpstream(c.upstream), nil
	}
}

func (c *serviceConnector) Done() <-chan struct{} {
	if c == nil || c.generation == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}

	return c.generation.Done()
}

func (c *serviceConnector) Err() error {
	if c == nil || c.generation == nil {
		return resource.ErrResourceUnavailable
	}

	return c.generation.Err()
}

func (c *serviceConnector) Close() {
	if c == nil {
		return
	}

	c.closeOnce.Do(func() {
		if c.generation != nil {
			c.generation.Close()
		}
		if c.owner != nil {
			c.owner.mu.Lock()
			delete(c.owner.connectors, c)
			c.owner.mu.Unlock()
		}
	})
}
