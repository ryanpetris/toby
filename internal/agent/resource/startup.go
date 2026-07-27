package resource

// Owns bounded, generation-specific startup contexts and publishes only fully
// ready instances.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errStartUnused = errors.New("resource startup has no opening leases")

func (r *Registry) startLocked(current *entry) error {
	generation, err := r.nextGenerationLocked()
	if err != nil {
		return err
	}

	r.cancelFailureRetentionLocked(current)

	startContext, cancelStart := context.WithCancelCause(r.lifetimeContext)
	startedAt := r.options.Clock.Now()
	startDeadline := startedAt.Add(r.options.StartTimeout)

	current.generation = generation
	current.state = StateStarting
	current.instance = nil
	current.reaped = false
	current.startPending = true
	current.startCancel = cancelStart
	current.startedAt = startedAt
	current.startDeadline = startDeadline
	current.readyAt = time.Time{}
	current.idleDeadline = time.Time{}
	current.retryDeadline = time.Time{}
	current.updatedAt = startedAt
	for lease := range current.leases {
		if lease.state == LeaseOpening {
			lease.generation = generation
		}
	}

	key := current.key
	current.startTimer = r.options.Clock.AfterFunc(r.options.StartTimeout, func() {
		r.expireStart(key, generation, startDeadline)
	})

	r.workers.Add(1)
	go r.startGeneration(startContext, key, generation, cancelStart)

	return nil
}

func (r *Registry) startGeneration(
	startContext context.Context,
	key Key,
	generation uint64,
	cancelStart context.CancelCauseFunc,
) {
	defer r.workers.Done()
	defer cancelStart(nil)

	instance, err := r.factory.Start(startContext, key, generation)
	if err == nil {
		err = context.Cause(startContext)
	}
	if err == nil && instance == nil {
		err = errors.New("resource factory returned a nil instance")
	}
	if err == nil && instance.Done() == nil {
		err = errors.New("resource instance returned a nil done channel")
	}
	if err == nil {
		select {
		case <-instance.Done():
			err = instance.Err()
			if err == nil {
				err = errors.New("resource exited before startup completed")
			}
		default:
		}
	}
	if err != nil {
		r.cleanupFailedStart(key, generation, err, instance)
		return
	}

	r.mu.Lock()
	current := r.entries[key]
	if current != nil && current.generation == generation && current.startPending && current.state != StateStarting {
		cause := context.Cause(startContext)
		if cause == nil {
			cause = ErrResourceUnavailable
		}
		r.mu.Unlock()
		r.cleanupFailedStart(key, generation, cause, instance)
		return
	}
	if r.closing || current == nil || current.generation != generation || current.state != StateStarting {
		r.mu.Unlock()
		r.stopDetached(instance)
		return
	}
	if !r.options.Clock.Now().Before(current.startDeadline) {
		r.timeoutStartLocked(current)
		r.mu.Unlock()
		r.cleanupFailedStart(key, generation, context.DeadlineExceeded, instance)
		return
	}

	r.cancelStartLocked(current, nil)
	current.startPending = false
	current.instance = instance
	current.readyAt = r.options.Clock.Now()
	current.retryDeadline = time.Time{}
	current.updatedAt = current.readyAt
	current.state = StateReady
	r.scheduleStableResetLocked(current)
	for lease := range current.leases {
		if lease.state == LeaseOpening && lease.generation == generation {
			r.activateLeaseLocked(current, lease)
		}
	}
	r.maybeIdleLocked(current)

	r.workers.Add(1)
	go r.watchGeneration(key, generation, instance)
	r.mu.Unlock()
}

func (r *Registry) cleanupFailedStart(key Key, generation uint64, cause error, instance Instance) {
	outcome := stopOutcome{reaped: true}
	if instance != nil {
		outcome = r.stopInstance(instance)
	}
	r.completeStartFailure(key, generation, cause, instance, outcome)
	if instance != nil && !outcome.reaped {
		<-instance.Done()
		r.generationExited(key, generation, instance.Err())
	}
}

func (r *Registry) expireStart(key Key, generation uint64, deadline time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.entries[key]
	if r.closing || current == nil || current.generation != generation || current.state != StateStarting {
		return
	}
	if current.startDeadline != deadline || r.options.Clock.Now().Before(deadline) {
		return
	}

	current.startTimer = nil
	r.timeoutStartLocked(current)
}

func (r *Registry) timeoutStartLocked(current *entry) {
	r.cancelStartLocked(current, context.DeadlineExceeded)
	r.failLocked(current, "resource start timed out")
	r.invalidateLeasesLocked(current, fmt.Errorf("start resource: %w", context.DeadlineExceeded))
}

func (r *Registry) cancelUnusedStartLocked(current *entry) {
	if current.state != StateStarting || len(current.leases) != 0 || len(current.connectors) != 0 {
		return
	}

	r.cancelStartLocked(current, errStartUnused)
	current.state = StateFailed
	current.instance = nil
	current.reaped = false
	current.readyAt = time.Time{}
	current.updatedAt = r.options.Clock.Now()
	current.lastError = ""
	if current.failures != 0 {
		current.retryDeadline = current.updatedAt
		r.scheduleFailureRetentionLocked(current)
	}
}

func (r *Registry) cancelStartLocked(current *entry, cause error) {
	if current.startTimer != nil {
		current.startTimer.Stop()
		current.startTimer = nil
	}
	if current.startCancel != nil {
		current.startCancel(cause)
		current.startCancel = nil
	}
	current.startDeadline = time.Time{}
}

func (r *Registry) completeStartFailure(
	key Key,
	generation uint64,
	cause error,
	instance Instance,
	cleanup stopOutcome,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.entries[key]
	if current == nil || current.generation != generation || !current.startPending {
		return
	}

	wasStarting := current.state == StateStarting
	if wasStarting {
		r.cancelStartLocked(current, cause)
	}
	if instance != nil {
		cleanup = cleanup.observeReap(instance.Done())
	}
	cleanupError := cleanup.Error()
	if cleanupError != nil && r.closing {
		r.shutdownErrors = append(r.shutdownErrors, cleanupError)
	}
	if cleanup.reaped {
		current.instance = nil
	} else {
		current.instance = instance
	}
	current.startPending = false
	if r.closing {
		current.updatedAt = r.options.Clock.Now()
		r.invalidateLeasesLocked(current, ErrShuttingDown)
		if cleanup.reaped {
			current.state = StateCold
			r.deleteColdEntryLocked(current)
		} else {
			current.state = StateFailed
			current.lastError = "failed resource start could not be reaped"
		}
		return
	}

	if !wasStarting {
		current.updatedAt = r.options.Clock.Now()
		if cleanupError != nil {
			current.lastError = "resource start cleanup failed"
		}
		if cleanup.reaped {
			r.deleteFailedEntryLocked(current)
		}
		return
	}

	r.failLocked(current, "resource start failed")
	if cleanupError != nil {
		current.lastError = "resource start cleanup failed"
	}
	r.invalidateLeasesLocked(current, fmt.Errorf("start resource: %w", cause))
}
