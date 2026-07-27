package resource

// Abstracts lifecycle time so idle expiry and retry backoff are deterministic
// in tests.

import "time"

// Timer is the cancelable timer contract used by Registry.
type Timer interface {
	// Stop prevents the timer callback when it has not fired.
	Stop() bool
}

// Clock supplies monotonic-aware time values and callback timers.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// AfterFunc schedules a callback after a duration.
	AfterFunc(time.Duration, func()) Timer
}

type realClock struct{}

var _ Clock = realClock{}

func (realClock) Now() time.Time {
	return time.Now()
}

func (realClock) AfterFunc(delay time.Duration, callback func()) Timer {
	return time.AfterFunc(delay, callback)
}
