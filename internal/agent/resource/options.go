package resource

// Defines and validates lifecycle timing, retry, and jitter policy.

import (
	"fmt"
	"math/rand/v2"
	"time"

	"petris.dev/toby/internal/shutdown"
)

const (
	defaultIdleTimeout      = 5 * time.Minute
	defaultStartTimeout     = 2 * time.Minute
	defaultBackoffInitial   = time.Second
	defaultBackoffMaximum   = time.Minute
	defaultFailureRetention = 5 * time.Minute
	defaultStableReady      = 30 * time.Second
)

// Jitter transforms an exponential retry delay. Registry clamps its result
// between half the exponential delay and BackoffMaximum.
type Jitter func(time.Duration) time.Duration

// Options controls resource lifecycle timing. Zero duration fields receive
// conservative defaults; negative durations are rejected.
type Options struct {
	IdleTimeout      time.Duration
	StartTimeout     time.Duration
	StopGrace        time.Duration
	KillGrace        time.Duration
	BackoffInitial   time.Duration
	BackoffMaximum   time.Duration
	FailureRetention time.Duration
	StableReady      time.Duration
	Clock            Clock
	Jitter           Jitter
}

func (o Options) normalized() (Options, error) {
	if err := validateNonNegativeDurations(o); err != nil {
		return Options{}, err
	}

	if o.IdleTimeout == 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.StartTimeout == 0 {
		o.StartTimeout = defaultStartTimeout
	}
	if o.StopGrace == 0 {
		o.StopGrace = shutdown.ResourceStopGrace
	}
	if o.KillGrace == 0 {
		o.KillGrace = shutdown.ResourceKillGrace
	}
	if o.BackoffInitial == 0 {
		o.BackoffInitial = defaultBackoffInitial
	}
	if o.BackoffMaximum == 0 {
		o.BackoffMaximum = defaultBackoffMaximum
	}
	if o.FailureRetention == 0 {
		o.FailureRetention = defaultFailureRetention
	}
	if o.StableReady == 0 {
		o.StableReady = defaultStableReady
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Jitter == nil {
		o.Jitter = defaultJitter
	}
	if o.BackoffMaximum < o.BackoffInitial {
		return Options{}, fmt.Errorf("resource backoff maximum must not be less than its initial delay")
	}

	return o, nil
}

func defaultJitter(delay time.Duration) time.Duration {
	minimum := delay / 2
	width := delay - minimum
	if width <= 0 {
		return delay
	}

	return minimum + time.Duration(rand.Int64N(int64(width)+1))
}

func validateNonNegativeDurations(options Options) error {
	durations := []struct {
		name  string
		value time.Duration
	}{
		{name: "idle timeout", value: options.IdleTimeout},
		{name: "start timeout", value: options.StartTimeout},
		{name: "stop grace", value: options.StopGrace},
		{name: "kill grace", value: options.KillGrace},
		{name: "backoff initial", value: options.BackoffInitial},
		{name: "backoff maximum", value: options.BackoffMaximum},
		{name: "failure retention", value: options.FailureRetention},
		{name: "stable readiness period", value: options.StableReady},
	}
	for _, duration := range durations {
		if duration.value < 0 {
			return fmt.Errorf("resource %s must not be negative", duration.name)
		}
	}

	return nil
}
