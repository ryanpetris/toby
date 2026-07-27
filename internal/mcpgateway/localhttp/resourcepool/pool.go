package resourcepool

// Owns canonical plan registration and the generic agent generation registry.

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/localhttp"
)

// Pool implements localhttp.Pool with generation-safe agent resource leases.
type Pool struct {
	builder *resource.Builder
	planner Planner
	starter Starter

	registry *resource.Registry
	logger   *diagnostic.Logger

	mu      sync.Mutex
	plans   map[resource.Key]*planEntry
	closing bool
}

var _ localhttp.Pool = (*Pool)(nil)
var _ resource.Factory = (*Pool)(nil)

type planEntry struct {
	plan Plan
	refs uint64
}

// New constructs an initially empty local HTTP process pool.
func New(
	builder *resource.Builder,
	planner Planner,
	starter Starter,
	options resource.Options,
	logger *diagnostic.Logger,
) (*Pool, error) {
	if builder == nil {
		return nil, fmt.Errorf("local HTTP MCP resource builder is required")
	}
	if isNilContract(planner) {
		return nil, fmt.Errorf("local HTTP MCP process planner is required")
	}
	if isNilContract(starter) {
		return nil, fmt.Errorf("local HTTP MCP process starter is required")
	}

	result := &Pool{
		builder: builder,
		planner: planner,
		starter: starter,
		plans:   make(map[resource.Key]*planEntry),
		logger:  logger,
	}
	registry, err := resource.NewRegistry(result, options)
	if err != nil {
		return nil, err
	}
	result.registry = registry

	return result, nil
}

// Prepare resolves and canonicalizes one definition without starting its
// process.
func (p *Pool) Prepare(
	ctx context.Context,
	definition localhttp.Definition,
	progress mcpgateway.ProgressReporter,
) (localhttp.Preparation, error) {
	if p == nil || p.registry == nil {
		return nil, fmt.Errorf("local HTTP MCP process pool is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("prepare local HTTP MCP process context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	closing := p.closing
	p.mu.Unlock()
	if closing {
		return nil, resource.ErrShuttingDown
	}

	plan, err := p.planner.Plan(
		ctx,
		cloneDefinition(definition),
		progress,
	)
	if err != nil {
		return nil, fmt.Errorf("plan local HTTP MCP process: %w", err)
	}
	if err := validatePlan(plan); err != nil {
		p.logger.DebugError(
			"close invalid local HTTP MCP process plan",
			closePlan(&plan),
		)
		return nil, err
	}
	key, err := p.builder.Build(plan.Resource)
	if err != nil {
		p.logger.DebugError(
			"close unkeyed local HTTP MCP process plan",
			closePlan(&plan),
		)
		return nil, fmt.Errorf("key local HTTP MCP process: %w", err)
	}

	return &prepared{
		pool: p,
		key:  key,
		plan: &plan,
	}, nil
}

// Start implements resource.Factory by looking up the already registered exact
// plan for key and delegating readiness to Starter.
func (p *Pool) Start(
	ctx context.Context,
	key resource.Key,
	generation uint64,
) (resource.Instance, error) {
	p.mu.Lock()
	entry := p.plans[key]
	if entry == nil || entry.refs == 0 {
		p.mu.Unlock()
		return nil, fmt.Errorf("local HTTP MCP process plan is unavailable")
	}
	plan := clonePlan(entry.plan)
	p.mu.Unlock()

	instance, err := p.starter.Start(ctx, plan, generation)
	if err != nil {
		return instance, err
	}
	if instance == nil {
		return nil, fmt.Errorf("local HTTP MCP starter returned nil")
	}
	if _, err := instance.Upstream(); err != nil {
		return instance, fmt.Errorf(
			"local HTTP MCP endpoint is not ready: %w",
			err,
		)
	}

	return instance, nil
}

// Shutdown refuses new plans and permanently shuts down every process
// generation.
func (p *Pool) Shutdown(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("local HTTP MCP process pool shutdown context is nil")
	}

	p.mu.Lock()
	p.closing = true
	p.mu.Unlock()

	if p.registry == nil {
		return nil
	}
	err := p.registry.Shutdown(ctx)

	p.mu.Lock()
	if len(p.plans) != 0 {
		err = errors.Join(
			err,
			fmt.Errorf(
				"local HTTP MCP process pool retained %d active plans",
				len(p.plans),
			),
		)
	}
	p.mu.Unlock()

	return err
}

func (p *Pool) register(key resource.Key, plan *Plan) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closing {
		return resource.ErrShuttingDown
	}
	current := p.plans[key]
	if current == nil {
		p.plans[key] = &planEntry{
			plan: clonePlan(*plan),
			refs: 1,
		}
		plan.Capabilities = nil
		return nil
	}
	p.logger.DebugError(
		"close duplicate local HTTP MCP process plan",
		closePlan(plan),
	)
	current.refs++

	return nil
}

func (p *Pool) unregister(key resource.Key) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	current := p.plans[key]
	if current == nil {
		return nil
	}
	if current.refs > 1 {
		current.refs--
		return nil
	}
	delete(p.plans, key)
	p.logger.DebugError(
		"close local HTTP MCP process plan",
		closePlan(&current.plan),
	)
	return nil
}
