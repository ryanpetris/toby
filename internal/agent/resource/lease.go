package resource

// Implements idempotent service leases and their resource-generation access.

import "fmt"

// Lease is one run's reference to a ready resource generation.
type Lease struct {
	registry *Registry
	key      Key

	state      LeaseState
	generation uint64
	ready      chan acquireResult
	done       chan struct{}
	err        error
}

// State reports the lease's current lifecycle state.
func (l *Lease) State() LeaseState {
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()

	return l.state
}

// Generation reports the resource generation assigned to the lease.
func (l *Lease) Generation() uint64 {
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()

	return l.generation
}

// Done closes when the lease is released or invalidated.
func (l *Lease) Done() <-chan struct{} {
	return l.done
}

// Err reports why the lease closed. A normal Release records no error.
func (l *Lease) Err() error {
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()

	return l.err
}

// Instance returns the ready generation supervised by this active lease.
func (l *Lease) Instance() (Instance, error) {
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()

	if l.state != LeaseActive {
		return nil, ErrLeaseClosed
	}

	current := l.registry.entries[l.key]
	if current == nil || current.generation != l.generation || current.state != StateReady || current.instance == nil {
		return nil, fmt.Errorf("%w: generation is not ready", ErrResourceUnavailable)
	}

	select {
	case <-current.instance.Done():
		return nil, ErrResourceExited
	default:
		return current.instance, nil
	}
}

// Release idempotently drops this run's reference to the resource.
func (l *Lease) Release() {
	r := l.registry
	r.mu.Lock()
	defer r.mu.Unlock()

	if l.state == LeaseClosed || l.state == LeaseReleasing {
		return
	}

	l.state = LeaseReleasing
	current := r.entries[l.key]
	if current != nil {
		delete(current.leases, l)
	}
	r.closeLeaseLocked(l, nil, false)

	if current != nil {
		r.cancelUnusedStartLocked(current)
		r.maybeIdleLocked(current)
	}
}

func (r *Registry) activateLeaseLocked(current *entry, lease *Lease) {
	lease.generation = current.generation
	lease.state = LeaseActive
	lease.ready <- acquireResult{}
}

func (r *Registry) closeLeaseLocked(lease *Lease, cause error, notifyOpening bool) {
	if lease.state == LeaseClosed {
		return
	}

	wasOpening := lease.state == LeaseOpening
	lease.state = LeaseClosed
	lease.err = cause
	if notifyOpening && wasOpening {
		lease.ready <- acquireResult{err: cause}
	}
	close(lease.done)
}

func (r *Registry) invalidateLeasesLocked(current *entry, cause error) {
	for lease := range current.leases {
		delete(current.leases, lease)
		r.closeLeaseLocked(lease, cause, true)
	}
}
