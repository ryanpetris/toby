package resource

// Performs graceful then forced generation termination without holding the
// registry mutex.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type stopOutcome struct {
	reaped   bool
	timedOut bool
	err      error
}

func (o stopOutcome) Error() error {
	if !o.timedOut {
		return o.err
	}
	return errors.Join(o.err, errors.New("resource did not exit within forced termination grace"))
}

func (o stopOutcome) observeReap(done <-chan struct{}) stopOutcome {
	if o.reaped {
		return o
	}

	select {
	case <-done:
		o.reaped = true
		o.timedOut = false
	default:
	}
	return o
}

func (r *Registry) beginStopLocked(current *entry) {
	r.cancelIdleLocked(current)
	r.cancelStableResetLocked(current)
	current.state = StateStopping
	current.reaped = false
	current.updatedAt = r.options.Clock.Now()

	key := current.key
	generation := current.generation
	instance := current.instance

	r.workers.Add(1)
	go r.stopGeneration(key, generation, instance)
}

func (r *Registry) stopGeneration(key Key, generation uint64, instance Instance) {
	defer r.workers.Done()

	outcome := r.stopInstance(instance)
	var done <-chan struct{}
	if instance != nil {
		done = instance.Done()
	}
	r.completeStop(key, generation, done, outcome)
}

func (r *Registry) completeStop(
	key Key,
	generation uint64,
	done <-chan struct{},
	outcome stopOutcome,
) {
	r.mu.Lock()
	defer r.mu.Unlock()

	current := r.entries[key]
	if current == nil || current.generation != generation || current.state != StateStopping {
		return
	}
	if current.reaped {
		outcome.reaped = true
		outcome.timedOut = false
	}
	outcome = outcome.observeReap(done)

	outcomeError := outcome.Error()
	if outcomeError != nil {
		if r.closing {
			r.shutdownErrors = append(r.shutdownErrors, outcomeError)
		}
		current.lastError = "resource stop failed"
	}
	if !outcome.reaped {
		r.invalidateLeasesLocked(current, ErrResourceUnavailable)
		r.invalidateConnectorsLocked(current, ErrResourceUnavailable)
		r.failLocked(current, "resource could not be reaped")
		return
	}

	current.instance = nil
	current.reaped = false
	current.failures = 0
	current.retryDeadline = time.Time{}
	current.updatedAt = r.options.Clock.Now()

	if r.closing {
		r.invalidateLeasesLocked(current, ErrShuttingDown)
		r.invalidateConnectorsLocked(current, ErrShuttingDown)
		current.state = StateCold
		r.deleteColdEntryLocked(current)
		return
	}
	if len(current.leases) != 0 {
		current.state = StateCold
		if err := r.startLocked(current); err != nil {
			r.invalidateLeasesLocked(current, err)
			r.failLocked(current, "resource restart failed")
		}
		return
	}

	current.state = StateCold
	r.deleteColdEntryLocked(current)
}

func (r *Registry) stopDetached(instance Instance) {
	outcome := r.stopInstance(instance).observeReap(instance.Done())
	outcomeError := outcome.Error()
	if outcomeError != nil {
		r.mu.Lock()
		if r.closing {
			r.shutdownErrors = append(r.shutdownErrors, outcomeError)
		}
		r.mu.Unlock()
	}
	if !outcome.reaped {
		<-instance.Done()
	}
}

func (r *Registry) stopInstance(instance Instance) stopOutcome {
	if instance == nil {
		return stopOutcome{reaped: true}
	}
	select {
	case <-instance.Done():
		return stopOutcome{reaped: true}
	default:
	}

	var collected []error

	stopContext, cancelStop := context.WithTimeout(context.Background(), r.options.StopGrace)
	stopError := instance.Stop(stopContext)
	if stopError != nil {
		collected = append(collected, fmt.Errorf("gracefully stop resource: %w", stopError))
	}
	reaped := waitForInstance(stopContext, instance.Done())
	cancelStop()
	if reaped {
		return stopOutcome{reaped: true, err: errors.Join(collected...)}
	}

	killContext, cancelKill := context.WithTimeout(context.Background(), r.options.KillGrace)
	killError := instance.Kill(killContext)
	if killError != nil {
		collected = append(collected, fmt.Errorf("force resource termination: %w", killError))
	}
	reaped = waitForInstance(killContext, instance.Done())
	cancelKill()

	return stopOutcome{
		reaped:   reaped,
		timedOut: !reaped,
		err:      errors.Join(collected...),
	}
}

func waitForInstance(ctx context.Context, done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
	}

	select {
	case <-done:
		return true
	case <-ctx.Done():
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
}
