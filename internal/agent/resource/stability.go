package resource

// Resets retry history only after a generation remains ready for the configured
// stability period.

func (r *Registry) scheduleStableResetLocked(current *entry) {
	r.cancelStableResetLocked(current)

	key := current.key
	generation := current.generation
	current.stableTimer = r.options.Clock.AfterFunc(r.options.StableReady, func() {
		r.resetStableGeneration(key, generation)
	})
}

func (r *Registry) cancelStableResetLocked(current *entry) {
	if current.stableTimer == nil {
		return
	}

	current.stableTimer.Stop()
	current.stableTimer = nil
}

func (r *Registry) resetStableGeneration(key Key, generation uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.entries[key]
	if r.closing || current == nil || current.generation != generation {
		return
	}
	if current.state != StateReady && current.state != StateIdle {
		return
	}

	current.stableTimer = nil
	current.failures = 0
	current.updatedAt = r.options.Clock.Now()
}
