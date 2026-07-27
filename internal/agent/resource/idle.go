package resource

// Starts, cancels, and validates generation-specific idle timers.

import "time"

func (r *Registry) maybeIdleLocked(current *entry) {
	if current.state != StateReady || len(current.leases) != 0 || len(current.connectors) != 0 {
		return
	}

	current.state = StateIdle
	current.idleDeadline = r.options.Clock.Now().Add(current.idleTimeout)
	current.updatedAt = r.options.Clock.Now()

	key := current.key
	generation := current.generation
	deadline := current.idleDeadline
	current.idleTimer = r.options.Clock.AfterFunc(current.idleTimeout, func() {
		r.expireIdle(key, generation, deadline)
	})
}

func (r *Registry) cancelIdleLocked(current *entry) {
	if current.idleTimer != nil {
		current.idleTimer.Stop()
		current.idleTimer = nil
	}
	current.idleDeadline = time.Time{}
}

func (r *Registry) expireIdle(key Key, generation uint64, deadline time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.entries[key]
	if r.closing || current == nil || current.generation != generation || current.state != StateIdle {
		return
	}
	if current.idleDeadline != deadline || r.options.Clock.Now().Before(deadline) {
		return
	}

	current.idleTimer = nil
	current.idleDeadline = time.Time{}
	r.beginStopLocked(current)
}
