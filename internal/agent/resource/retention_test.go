package resource

// Verifies that run-scoped resource churn does not retain quiescent entries.

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestRegistryEvictsQuiescentRunScopedEntries(t *testing.T) {
	const resourceCount = 300

	clock := newFakeClock()
	factory := &fakeFactory{}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	for index := range resourceCount {
		key := runScopedLifecycleKey(uint16(index))
		lease, err := registry.AcquireWithPolicy(t.Context(), key, AcquisitionPolicy{})
		if err != nil {
			t.Fatalf("Acquire resource %d: %v", index, err)
		}
		lease.Release()
	}

	statuses := registry.status()
	if len(statuses) != resourceCount {
		t.Fatalf("idle resource count = %d, want %d", len(statuses), resourceCount)
	}
	for _, status := range statuses {
		if status.State != StateIdle {
			t.Fatalf("resource %s state = %q, want %q", status.Key.ID, status.State, StateIdle)
		}
	}

	clock.Advance(lifecycleOptions(clock).IdleTimeout)
	waitForRegistryEmpty(t, registry)

	registry.mu.Lock()
	retained := len(registry.entries)
	registry.mu.Unlock()
	if retained != 0 {
		t.Fatalf("retained entry count = %d, want 0", retained)
	}
	if starts := factory.StartCount(); starts != resourceCount {
		t.Fatalf("factory start count = %d, want %d", starts, resourceCount)
	}
}

func TestRegistryEvictsNewColdEntryWhenGenerationReservationFails(t *testing.T) {
	clock := newFakeClock()
	registry, err := NewRegistry(&fakeFactory{}, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	registry.nextGeneration = math.MaxUint64
	if _, err := registry.AcquireWithPolicy(t.Context(), runScopedLifecycleKey(1), AcquisitionPolicy{}); err == nil {
		t.Fatal("Acquire succeeded after generation space exhaustion")
	}

	if statuses := registry.status(); len(statuses) != 0 {
		t.Fatalf("status after failed reservation = %+v, want no retained entry", statuses)
	}
}

func TestRegistryEventuallyEvictsManyQuiescentFailedEntries(t *testing.T) {
	const resourceCount = 300

	clock := newFakeClock()
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			return nil, errors.New("start failed")
		},
	}
	options := lifecycleOptions(clock)
	options.Jitter = func(delay time.Duration) time.Duration {
		return delay
	}
	options.FailureRetention = 10 * time.Second
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	for index := range resourceCount {
		key := runScopedLifecycleKey(uint16(index))
		if _, err := registry.AcquireWithPolicy(t.Context(), key, AcquisitionPolicy{}); err == nil {
			t.Fatalf("Acquire resource %d succeeded", index)
		}
	}

	statuses := registry.status()
	if len(statuses) != resourceCount {
		t.Fatalf("failed resource count = %d, want %d", len(statuses), resourceCount)
	}
	for _, status := range statuses {
		if status.State != StateFailed {
			t.Fatalf("resource %s state = %q, want %q", status.Key.ID, status.State, StateFailed)
		}
	}

	clock.Advance(options.BackoffInitial + options.FailureRetention)
	waitForRegistryEmpty(t, registry)
}

func TestRegistryFailureExpiryCannotEvictReplacementGeneration(t *testing.T) {
	clock := newFakeClock()
	var attempts int
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("first start failed")
			}
			return newFakeInstance(), nil
		},
	}
	options := lifecycleOptions(clock)
	options.Jitter = func(delay time.Duration) time.Duration {
		return delay
	}
	options.FailureRetention = 10 * time.Second
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	key := lifecycleKey(1)
	if _, err := registry.AcquireWithPolicy(t.Context(), key, AcquisitionPolicy{}); err == nil {
		t.Fatal("first Acquire succeeded")
	}

	registry.mu.Lock()
	failedGeneration := registry.entries[key].generation
	failedDeadline := registry.entries[key].failureDeadline
	registry.mu.Unlock()

	clock.Advance(options.BackoffInitial)
	lease, err := registry.AcquireWithPolicy(t.Context(), key, AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if lease.Generation() == failedGeneration {
		t.Fatalf("replacement generation = %d, want a new generation", lease.Generation())
	}

	clock.Advance(options.FailureRetention)
	registry.expireFailure(key, failedGeneration, failedDeadline)
	status := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateReady
	})
	if status.Generation != lease.Generation() {
		t.Fatalf("status generation = %d, want %d", status.Generation, lease.Generation())
	}
	lease.Release()
}

func runScopedLifecycleKey(seed uint16) Key {
	var digest [32]byte
	digest[0] = byte(seed)
	digest[1] = byte(seed >> 8)

	return Key{
		digest:    digest,
		kind:      KindMCPHTTP,
		transport: TransportHTTP,
		scope:     ScopeRun,
	}
}
