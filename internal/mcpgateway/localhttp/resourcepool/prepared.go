package resourcepool

// Registers a resolved plan only for the duration of an acquired generation
// lease, preventing abandoned Prepare calls from retaining secret plans.

import (
	"context"
	"fmt"
	"sync"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway/localhttp"
)

type prepared struct {
	pool *Pool
	key  resource.Key

	mu     sync.Mutex
	plan   *Plan
	closed bool
}

var _ localhttp.Preparation = (*prepared)(nil)

func (p *prepared) Acquire(
	ctx context.Context,
) (localhttp.ServiceLease, error) {
	if p == nil || p.pool == nil || p.pool.registry == nil {
		return nil, fmt.Errorf("prepared local HTTP MCP process is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("acquire local HTTP MCP process context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed || p.plan == nil {
		p.mu.Unlock()
		return nil, fmt.Errorf(
			"prepared local HTTP MCP process is closed",
		)
	}
	plan := p.plan
	p.plan = nil
	p.closed = true
	p.mu.Unlock()

	if err := p.pool.register(p.key, plan); err != nil {
		p.pool.logger.DebugError(
			"close unregistered local HTTP MCP process plan",
			closePlan(plan),
		)
		return nil, err
	}
	lease, err := p.pool.registry.AcquireWithPolicy(
		ctx,
		p.key,
		resource.AcquisitionPolicy{
			IdleTimeout: plan.Definition.IdleTimeout.Duration,
		},
	)
	if err != nil {
		p.pool.logger.DebugError(
			"unregister local HTTP MCP process plan after acquisition failure",
			p.pool.unregister(p.key),
		)
		return nil, err
	}

	result := &serviceLease{
		pool:        p.pool,
		key:         p.key,
		lease:       lease,
		idleTimeout: plan.Definition.IdleTimeout.Duration,
		accepting:   true,
		connectors:  make(map[*serviceConnector]struct{}),
	}
	if _, err := lease.Instance(); err != nil {
		lease.Release()
		p.pool.logger.DebugError(
			"unregister inaccessible local HTTP MCP process plan",
			p.pool.unregister(p.key),
		)
		return nil, fmt.Errorf(
			"access ready local HTTP MCP process: %w",
			err,
		)
	}

	return result, nil
}

// Close discards a process plan that was never acquired. After Acquire
// transfers the plan into the pool, Close is a no-op.
func (p *prepared) Close() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	plan := p.plan
	p.plan = nil
	p.mu.Unlock()

	p.pool.logger.DebugError(
		"close unused local HTTP MCP process plan",
		closePlan(plan),
	)
	return nil
}
