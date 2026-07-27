package resource

// Coordinates canonical resource entries while keeping slow lifecycle work
// outside the registry mutex.

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// Registry owns the state, leases, connectors, and process generations for
// canonical reusable resources.
type Registry struct {
	factory Factory
	options Options

	lifetimeContext context.Context
	cancelLifetime  context.CancelFunc

	mu             sync.Mutex
	entries        map[Key]*entry
	nextGeneration uint64
	closing        bool
	shutdownWait   bool
	shutdownDone   chan struct{}
	shutdownErrors []error
	workers        sync.WaitGroup
}

type entry struct {
	key          Key
	generation   uint64
	state        State
	instance     Instance
	reaped       bool
	startPending bool

	leases     map[*Lease]struct{}
	connectors map[*Connector]struct{}

	startCancel  context.CancelCauseFunc
	startTimer   Timer
	idleTimer    Timer
	stableTimer  Timer
	failureTimer Timer

	startedAt       time.Time
	startDeadline   time.Time
	readyAt         time.Time
	idleDeadline    time.Time
	idleTimeout     time.Duration
	retryDeadline   time.Time
	failureDeadline time.Time
	updatedAt       time.Time
	lastError       string
	failures        uint32
}

// NewRegistry constructs an initially empty resource lifecycle registry.
func NewRegistry(factory Factory, options Options) (*Registry, error) {
	if factory == nil {
		return nil, fmt.Errorf("resource factory is required")
	}

	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}

	lifetimeContext, cancelLifetime := context.WithCancel(context.Background())

	return &Registry{
		factory:         factory,
		options:         normalized,
		lifetimeContext: lifetimeContext,
		cancelLifetime:  cancelLifetime,
		entries:         make(map[Key]*entry),
		shutdownDone:    make(chan struct{}),
	}, nil
}

func (r *Registry) entryWithIdleLocked(
	key Key,
	idleTimeout time.Duration,
) (*entry, error) {
	current := r.entries[key]
	if current != nil {
		if current.idleTimeout != idleTimeout {
			return nil, fmt.Errorf(
				"resource %s idle policy does not match its existing entry",
				key.Summary().ID,
			)
		}
		return current, nil
	}

	current = &entry{
		key:         key,
		idleTimeout: idleTimeout,
		state:       StateCold,
		leases:      make(map[*Lease]struct{}),
		connectors:  make(map[*Connector]struct{}),
		updatedAt:   r.options.Clock.Now(),
	}
	r.entries[key] = current

	return current, nil
}

func (r *Registry) nextGenerationLocked() (uint64, error) {
	if r.nextGeneration == math.MaxUint64 {
		return 0, fmt.Errorf("resource generation space exhausted")
	}

	r.nextGeneration++
	return r.nextGeneration, nil
}

func validKey(key Key) bool {
	return key.kind != "" && key.transport != "" && key.scope != ""
}
