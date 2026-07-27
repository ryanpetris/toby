package resource

// Refuses new work and coordinates final termination and reaping of every
// resource generation.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Shutdown permanently closes the registry. Cleanup continues if ctx expires,
// and a later call may wait for the same shutdown to finish.
func (r *Registry) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("resource shutdown context is required")
	}

	r.mu.Lock()
	if !r.closing {
		r.beginShutdownLocked()
	}
	done := r.shutdownDone
	r.mu.Unlock()

	select {
	case <-done:
		r.mu.Lock()
		err := errors.Join(r.shutdownErrors...)
		r.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registry) beginShutdownLocked() {
	r.closing = true
	r.cancelLifetime()

	for _, current := range r.entries {
		r.cancelStartLocked(current, ErrShuttingDown)
		r.cancelIdleLocked(current)
		r.cancelStableResetLocked(current)
		r.cancelFailureRetentionLocked(current)
		r.invalidateLeasesLocked(current, ErrShuttingDown)
		r.invalidateConnectorsLocked(current, ErrShuttingDown)

		switch current.state {
		case StateReady, StateIdle, StateFailed:
			if current.instance != nil {
				r.beginStopLocked(current)
			} else {
				current.state = StateCold
				current.updatedAt = r.options.Clock.Now()
			}
		case StateStarting, StateStopping:
			// Existing workers finish or cancel these transitions.
		case StateCold:
			current.updatedAt = r.options.Clock.Now()
		}
	}

	if !r.shutdownWait {
		r.shutdownWait = true
		go func() {
			r.workers.Wait()

			r.mu.Lock()
			for _, current := range r.entries {
				current.state = StateCold
				current.instance = nil
				current.reaped = false
				current.startPending = false
				current.startDeadline = time.Time{}
				current.idleDeadline = time.Time{}
				current.retryDeadline = time.Time{}
				current.failureDeadline = time.Time{}
				current.updatedAt = r.options.Clock.Now()
			}
			close(r.shutdownDone)
			r.mu.Unlock()
		}()
	}
}
