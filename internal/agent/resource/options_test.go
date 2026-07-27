package resource

// Verifies lifecycle timing validation and the bounded default jitter policy.

import (
	"testing"
	"time"
)

func TestOptionsRejectInvalidBackoffRange(t *testing.T) {
	_, err := NewRegistry(&fakeFactory{}, Options{
		BackoffInitial: 2 * time.Second,
		BackoffMaximum: time.Second,
	})
	if err == nil {
		t.Fatal("NewRegistry accepted a maximum below its initial backoff")
	}
}

func TestOptionsRejectNegativeGenerationTiming(t *testing.T) {
	tests := []Options{
		{StartTimeout: -time.Nanosecond},
		{FailureRetention: -time.Nanosecond},
	}
	for _, options := range tests {
		if _, err := NewRegistry(&fakeFactory{}, options); err == nil {
			t.Fatalf("NewRegistry accepted invalid options %+v", options)
		}
	}
}

func TestDefaultJitterStaysWithinHalfToFullDelay(t *testing.T) {
	const delay = 10 * time.Second
	for range 100 {
		got := defaultJitter(delay)
		if got < delay/2 || got > delay {
			t.Fatalf("defaultJitter = %s, want [5s, 10s]", got)
		}
	}
}
