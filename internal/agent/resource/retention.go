package resource

// Evicts cold entries immediately and failed generations after a bounded,
// generation-specific observation period.

import "time"

func (r *Registry) deleteColdEntryLocked(current *entry) bool {
	if current == nil || r.entries[current.key] != current {
		return false
	}
	if current.state != StateCold || current.instance != nil || current.reaped {
		return false
	}
	if current.startPending {
		return false
	}
	if len(current.leases) != 0 || len(current.connectors) != 0 {
		return false
	}
	if current.startCancel != nil || current.startTimer != nil || current.idleTimer != nil ||
		current.stableTimer != nil || current.failureTimer != nil {
		return false
	}
	if !current.startDeadline.IsZero() || !current.idleDeadline.IsZero() ||
		!current.retryDeadline.IsZero() || !current.failureDeadline.IsZero() {
		return false
	}

	delete(r.entries, current.key)
	return true
}

func (r *Registry) scheduleFailureRetentionLocked(current *entry) {
	r.cancelFailureRetentionLocked(current)

	current.failureDeadline = current.retryDeadline.Add(r.options.FailureRetention)
	key := current.key
	generation := current.generation
	deadline := current.failureDeadline
	delay := deadline.Sub(r.options.Clock.Now())
	if delay < 0 {
		delay = 0
	}
	current.failureTimer = r.options.Clock.AfterFunc(delay, func() {
		r.expireFailure(key, generation, deadline)
	})
}

func (r *Registry) cancelFailureRetentionLocked(current *entry) {
	if current.failureTimer != nil {
		current.failureTimer.Stop()
		current.failureTimer = nil
	}
	current.failureDeadline = time.Time{}
}

func (r *Registry) expireFailure(key Key, generation uint64, deadline time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.entries[key]
	if r.closing || current == nil || current.generation != generation || current.state != StateFailed {
		return
	}
	if current.failureDeadline != deadline || r.options.Clock.Now().Before(deadline) {
		return
	}

	current.failureTimer = nil
	r.deleteFailedEntryLocked(current)
}

func (r *Registry) deleteFailedEntryLocked(current *entry) bool {
	if current == nil || r.entries[current.key] != current {
		return false
	}
	if current.state != StateFailed || current.instance != nil {
		return false
	}
	if current.startPending {
		return false
	}
	if len(current.leases) != 0 || len(current.connectors) != 0 {
		return false
	}
	if r.options.Clock.Now().Before(current.failureDeadline) {
		return false
	}
	if current.failureTimer != nil {
		current.failureTimer.Stop()
		current.failureTimer = nil
	}

	delete(r.entries, current.key)
	return true
}
