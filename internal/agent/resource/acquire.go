package resource

// Reserves service leases and joins concurrent callers to one generation startup.

import (
	"context"
	"fmt"
	"time"
)

type acquireResult struct {
	err error
}

// AcquisitionPolicy supplies lifecycle behavior that is intentionally part of
// the caller's canonical resource identity.
type AcquisitionPolicy struct {
	IdleTimeout time.Duration
}

// AcquireWithPolicy reserves a lease using an explicit per-resource idle
// timeout. Callers must include the same timeout in the key's Spec.
func (r *Registry) AcquireWithPolicy(
	ctx context.Context,
	key Key,
	policy AcquisitionPolicy,
) (*Lease, error) {
	if ctx == nil {
		return nil, fmt.Errorf("resource acquire context is required")
	}
	if !validKey(key) {
		return nil, fmt.Errorf("valid resource key is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	idleTimeout := policy.IdleTimeout
	if idleTimeout == 0 {
		idleTimeout = r.options.IdleTimeout
	}
	if idleTimeout < 0 {
		return nil, fmt.Errorf(
			"resource idle timeout must not be negative",
		)
	}

	r.mu.Lock()
	lease, err := r.reserveLocked(key, idleTimeout)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case result := <-lease.ready:
		if result.err != nil {
			return nil, result.err
		}
		return lease, nil
	case <-ctx.Done():
		lease.Release()
		return nil, ctx.Err()
	}
}

func (r *Registry) reserveLocked(
	key Key,
	idleTimeout time.Duration,
) (*Lease, error) {
	if r.closing {
		return nil, ErrShuttingDown
	}

	current, err := r.entryWithIdleLocked(key, idleTimeout)
	if err != nil {
		return nil, err
	}
	now := r.options.Clock.Now()
	if current.startPending && current.state != StateStarting {
		return nil, ErrResourceUnavailable
	}

	switch current.state {
	case StateCold:
	case StateFailed:
		if current.instance != nil {
			return nil, ErrResourceUnavailable
		}
		if now.Before(current.retryDeadline) {
			return nil, &RetryError{RetryAt: current.retryDeadline}
		}
	case StateIdle:
		r.cancelIdleLocked(current)
		current.state = StateReady
		current.updatedAt = now
	case StateStarting, StateReady, StateStopping:
	default:
		return nil, fmt.Errorf("resource %s has invalid state %q", key.Summary().ID, current.state)
	}

	lease := &Lease{
		registry: r,
		key:      key,
		state:    LeaseOpening,
		ready:    make(chan acquireResult, 1),
		done:     make(chan struct{}),
	}
	current.leases[lease] = struct{}{}

	switch current.state {
	case StateCold, StateFailed:
		if err := r.startLocked(current); err != nil {
			delete(current.leases, lease)
			r.closeLeaseLocked(lease, err, true)
			r.deleteColdEntryLocked(current)
			return nil, err
		}
	case StateReady:
		r.activateLeaseLocked(current, lease)
	case StateStarting:
		lease.generation = current.generation
	case StateStopping:
		// The lease remains opening and is attached to the next generation after
		// the current generation has been reaped.
	default:
		err := fmt.Errorf(
			"resource %s entered invalid state %q while reserving a lease",
			key.Summary().ID,
			current.state,
		)
		delete(current.leases, lease)
		r.closeLeaseLocked(lease, err, true)
		return nil, err
	}

	return lease, nil
}
