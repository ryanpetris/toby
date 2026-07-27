package resource

// Applies generation-safe unexpected-exit handling and bounded retry backoff.

import (
	"fmt"
	"time"
)

func (r *Registry) watchGeneration(key Key, generation uint64, instance Instance) {
	defer r.workers.Done()

	<-instance.Done()
	r.generationExited(key, generation, instance.Err())
}

func (r *Registry) generationExited(key Key, generation uint64, cause error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.entries[key]
	if current == nil || current.generation != generation {
		return
	}

	switch current.state {
	case StateStopping:
		// stopGeneration owns the expected-exit transition, but its bounded
		// wait may have selected its deadline concurrently with this reap.
		current.reaped = true
		current.updatedAt = r.options.Clock.Now()
		return
	case StateFailed:
		// A force-stop timeout leaves the handle attached until its eventual
		// reap, but it must not disturb a replacement generation.
		current.instance = nil
		current.updatedAt = r.options.Clock.Now()
		r.deleteFailedEntryLocked(current)
		return
	case StateReady, StateIdle:
	default:
		return
	}

	r.cancelIdleLocked(current)
	r.cancelStableResetLocked(current)
	current.instance = nil

	exitError := ErrResourceExited
	if cause != nil {
		exitError = fmt.Errorf("%w: %v", ErrResourceExited, cause)
	}
	r.invalidateLeasesLocked(current, exitError)
	r.invalidateConnectorsLocked(current, exitError)
	r.failLocked(current, "resource exited unexpectedly")
}

func (r *Registry) failLocked(current *entry, sanitizedError string) {
	now := r.options.Clock.Now()
	if !current.readyAt.IsZero() && now.Sub(current.readyAt) >= r.options.StableReady {
		current.failures = 0
	}
	if current.failures < ^uint32(0) {
		current.failures++
	}

	base := r.options.BackoffInitial
	for remaining := current.failures; remaining > 1 && base < r.options.BackoffMaximum; remaining-- {
		if base > r.options.BackoffMaximum/2 {
			base = r.options.BackoffMaximum
			break
		}
		base *= 2
	}
	if base > r.options.BackoffMaximum {
		base = r.options.BackoffMaximum
	}

	delay := r.options.Jitter(base)
	minimumDelay := base / 2
	if minimumDelay <= 0 {
		minimumDelay = time.Nanosecond
	}
	if delay < minimumDelay {
		delay = minimumDelay
	}
	if delay > r.options.BackoffMaximum {
		delay = r.options.BackoffMaximum
	}

	current.state = StateFailed
	current.retryDeadline = now.Add(delay)
	current.updatedAt = now
	current.lastError = sanitizedError
	current.idleDeadline = time.Time{}
	r.scheduleFailureRetentionLocked(current)
}
