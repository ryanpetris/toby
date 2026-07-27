package resource

// Verifies shutdown cancellation, user invalidation, process reaping, and
// permanent refusal of new work.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegistryShutdownCancelsStartsAndReapsReadyGenerations(t *testing.T) {
	clock := newFakeClock()
	blockedKey := lifecycleKey(3)
	blockedStart := make(chan struct{})
	var mu sync.Mutex
	instances := make(map[Key]*fakeInstance)
	factory := &fakeFactory{
		startFn: func(ctx context.Context, key Key, _ uint64) (Instance, error) {
			if key == blockedKey {
				close(blockedStart)
				<-ctx.Done()
				return nil, ctx.Err()
			}

			instance := newFakeInstance()
			mu.Lock()
			instances[key] = instance
			mu.Unlock()
			return instance, nil
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}

	first, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	firstConnector, err := first.OpenConnector()
	if err != nil {
		t.Fatal(err)
	}
	second, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(2), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}

	blockedResult := make(chan error, 1)
	go func() {
		_, err := registry.AcquireWithPolicy(t.Context(), blockedKey, AcquisitionPolicy{})
		blockedResult <- err
	}()
	waitForSignal(t, blockedStart)

	shutdownContext, cancelShutdown := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelShutdown()
	if err := registry.Shutdown(shutdownContext); err != nil {
		t.Fatal(err)
	}

	requireErrorIs(t, <-blockedResult, ErrShuttingDown)
	waitForSignal(t, first.Done())
	waitForSignal(t, firstConnector.Done())
	waitForSignal(t, second.Done())
	requireErrorIs(t, first.Err(), ErrShuttingDown)
	requireErrorIs(t, firstConnector.Err(), ErrShuttingDown)
	requireErrorIs(t, second.Err(), ErrShuttingDown)

	mu.Lock()
	for key, instance := range instances {
		select {
		case <-instance.Done():
		default:
			t.Errorf("instance %s was not reaped", key.Summary().ID)
		}
	}
	mu.Unlock()

	if _, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(4), AcquisitionPolicy{}); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Acquire after shutdown = %v, want ErrShuttingDown", err)
	}
	for _, status := range registry.status() {
		if status.State != StateCold {
			t.Errorf("status state after shutdown = %q, want %q", status.State, StateCold)
		}
		if status.Leases != 0 || status.Connectors != 0 {
			t.Errorf("status retained users after shutdown: %+v", status)
		}
	}
}

func TestRegistryShutdownDoesNotFinishBeforeLateReap(t *testing.T) {
	clock := newFakeClock()
	instance := newFakeInstance()
	instance.stopFn = func(context.Context) error {
		return nil
	}
	instance.killFn = func(context.Context) error {
		return nil
	}
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			return instance, nil
		},
	}
	options := lifecycleOptions(clock)
	options.StopGrace = 5 * time.Millisecond
	options.KillGrace = 5 * time.Millisecond
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}

	lease, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}

	firstContext, cancelFirst := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancelFirst()
	if err := registry.Shutdown(firstContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown before reap = %v, want context deadline", err)
	}
	waitForSignal(t, lease.done)
	requireErrorIs(t, lease.Err(), ErrShuttingDown)
	if _, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(2), AcquisitionPolicy{}); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Acquire during late reap = %v, want ErrShuttingDown", err)
	}

	instance.Exit(nil)
	secondContext, cancelSecond := context.WithTimeout(t.Context(), time.Second)
	defer cancelSecond()
	if err := registry.Shutdown(secondContext); err == nil {
		t.Fatal("Shutdown after late reap did not report forced-termination timeout")
	}
	for _, status := range registry.status() {
		if status.State != StateCold {
			t.Fatalf("state after late reap = %q, want %q", status.State, StateCold)
		}
	}
}

func TestRegistryShutdownReapsInstanceThatBecomesReadyAfterCancellation(t *testing.T) {
	clock := newFakeClock()
	started := make(chan struct{})
	returnReady := make(chan struct{})
	instance := newFakeInstance()
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			close(started)
			<-returnReady
			return instance, nil
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}

	acquireResult := make(chan error, 1)
	go func() {
		_, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
		acquireResult <- err
	}()
	waitForSignal(t, started)

	shutdownContext, cancelShutdown := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelShutdown()
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- registry.Shutdown(shutdownContext)
	}()

	requireErrorIs(t, <-acquireResult, ErrShuttingDown)
	close(returnReady)
	if err := <-shutdownResult; err != nil {
		t.Fatal(err)
	}
	select {
	case <-instance.Done():
	default:
		t.Fatal("late ready instance was not reaped")
	}
}
