package resource

// Verifies failure invalidation, sanitized introspection, bounded exponential
// retry, stable-readiness reset, and forced idle termination.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistryUnexpectedExitInvalidatesUsersAndBacksOff(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	var attempt int
	var instances []*fakeInstance
	var jitterBases []time.Duration
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			mu.Lock()
			defer mu.Unlock()

			attempt++
			if attempt == 2 {
				return nil, errors.New("sensitive startup detail")
			}
			instance := newFakeInstance()
			instances = append(instances, instance)
			return instance, nil
		},
	}
	options := lifecycleOptions(clock)
	options.Jitter = func(base time.Duration) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		jitterBases = append(jitterBases, base)
		return base / 2
	}
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	first, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	connector, err := first.OpenConnector()
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	firstInstance := instances[0]
	mu.Unlock()
	firstInstance.Exit(errors.New("sensitive process detail"))

	waitForSignal(t, first.Done())
	waitForSignal(t, connector.Done())
	requireErrorIs(t, first.Err(), ErrResourceExited)
	requireErrorIs(t, connector.Err(), ErrResourceExited)

	failed := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateFailed && status.ConsecutiveFailures == 1
	})
	if failed.LastError != "resource exited unexpectedly" {
		t.Fatalf("LastError = %q", failed.LastError)
	}
	document, err := json.Marshal(failed)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"sensitive process detail", "sensitive startup detail"} {
		if strings.Contains(string(document), secret) {
			t.Fatalf("safe status leaked %q: %s", secret, document)
		}
	}

	if _, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{}); !errors.Is(err, ErrResourceUnavailable) {
		t.Fatalf("Acquire during backoff = %v, want ErrResourceUnavailable", err)
	}

	clock.Advance(2 * time.Second)
	if _, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{}); err == nil ||
		!strings.Contains(err.Error(), "sensitive startup detail") {
		t.Fatalf("retry startup error = %v, want factory cause", err)
	}
	secondFailure := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateFailed && status.ConsecutiveFailures == 2
	})
	if got := secondFailure.RetryDeadline.Sub(clock.Now()); got != 4*time.Second {
		t.Fatalf("second retry delay = %s, want 4s", got)
	}

	mu.Lock()
	gotBases := append([]time.Duration(nil), jitterBases...)
	mu.Unlock()
	if len(gotBases) != 2 || gotBases[0] != 4*time.Second || gotBases[1] != 8*time.Second {
		t.Fatalf("jitter bases = %v, want [4s 8s]", gotBases)
	}

	clock.Advance(4 * time.Second)
	third, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(20 * time.Second)

	mu.Lock()
	thirdInstance := instances[1]
	mu.Unlock()
	thirdInstance.Exit(errors.New("another failure"))
	waitForSignal(t, third.Done())
	stableFailure := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateFailed
	})
	if stableFailure.ConsecutiveFailures != 1 {
		t.Fatalf("failures after stable readiness = %d, want 1", stableFailure.ConsecutiveFailures)
	}
}

func TestRegistryIdleExpiryEscalatesToBoundedKill(t *testing.T) {
	clock := newFakeClock()
	instance := newFakeInstance()
	instance.stopFn = func(context.Context) error {
		return nil
	}
	instance.killFn = func(context.Context) error {
		instance.Exit(nil)
		return nil
	}
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			return instance, nil
		},
	}
	options := lifecycleOptions(clock)
	options.StopGrace = 10 * time.Millisecond
	options.KillGrace = 100 * time.Millisecond
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	lease, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	clock.Advance(10 * time.Second)

	waitForRegistryEmpty(t, registry)
	stops, kills := instance.Counts()
	if stops != 1 || kills != 1 {
		t.Fatalf("stop/kill counts = %d/%d, want 1/1", stops, kills)
	}
}

func TestRegistryReapsPartialInstanceReturnedWithStartFailure(t *testing.T) {
	clock := newFakeClock()
	instance := newFakeInstance()
	instance.stopFn = func(context.Context) error {
		return nil
	}
	instance.killFn = func(context.Context) error {
		instance.Exit(nil)
		return nil
	}
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			return instance, errors.New("startup failed")
		},
	}
	options := lifecycleOptions(clock)
	options.StopGrace = 10 * time.Millisecond
	options.KillGrace = 100 * time.Millisecond
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	if _, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{}); err == nil {
		t.Fatal("Acquire accepted a failed factory start")
	}
	select {
	case <-instance.Done():
	default:
		t.Fatal("partial start instance was not reaped")
	}
	stops, kills := instance.Counts()
	if stops != 1 || kills != 1 {
		t.Fatalf("stop/kill counts = %d/%d, want 1/1", stops, kills)
	}

	status := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateFailed
	})
	if status.LastError != "resource start failed" {
		t.Fatalf("LastError = %q, want sanitized start failure", status.LastError)
	}
}
