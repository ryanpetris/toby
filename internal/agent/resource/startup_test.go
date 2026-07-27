package resource

// Verifies generation-owned startup cancellation, deadlines, and shared
// readiness waiting.

import (
	"context"
	"errors"
	"testing"
	"time"
)

type acquireCallResult struct {
	lease *Lease
	err   error
}

func TestRegistryKeepsSharedStartWhileOpeningLeaseRemains(t *testing.T) {
	clock := newFakeClock()
	startContext := make(chan context.Context, 1)
	continueStart := make(chan struct{})
	factory := &fakeFactory{
		startFn: func(ctx context.Context, _ Key, _ uint64) (Instance, error) {
			startContext <- ctx
			select {
			case <-continueStart:
				return newFakeInstance(), nil
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	firstContext, cancelFirst := context.WithCancel(t.Context())
	secondContext, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()

	firstResult := make(chan acquireCallResult, 1)
	secondResult := make(chan acquireCallResult, 1)
	go func() {
		lease, err := registry.AcquireWithPolicy(firstContext, lifecycleKey(1), AcquisitionPolicy{})
		firstResult <- acquireCallResult{lease: lease, err: err}
	}()
	go func() {
		lease, err := registry.AcquireWithPolicy(secondContext, lifecycleKey(1), AcquisitionPolicy{})
		secondResult <- acquireCallResult{lease: lease, err: err}
	}()

	generationContext := <-startContext
	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateStarting && status.OpeningLeases == 2
	})

	cancelFirst()
	first := <-firstResult
	if first.lease != nil || !errors.Is(first.err, context.Canceled) {
		t.Fatalf("first Acquire = (%v, %v), want nil, context.Canceled", first.lease, first.err)
	}
	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateStarting && status.OpeningLeases == 1
	})
	if err := generationContext.Err(); err != nil {
		t.Fatalf("shared start canceled with one opening lease: %v", err)
	}
	if starts := factory.StartCount(); starts != 1 {
		t.Fatalf("Factory.Start count = %d, want 1", starts)
	}

	close(continueStart)
	second := <-secondResult
	if second.err != nil {
		t.Fatal(second.err)
	}
	if second.lease == nil {
		t.Fatal("second Acquire returned a nil lease")
	}
	second.lease.Release()
}

func TestRegistryCancelsStartWhenLastOpeningLeaseReleases(t *testing.T) {
	clock := newFakeClock()
	started := make(chan struct{})
	startCanceled := make(chan error, 1)
	factory := &fakeFactory{
		startFn: func(ctx context.Context, _ Key, _ uint64) (Instance, error) {
			close(started)
			<-ctx.Done()
			startCanceled <- context.Cause(ctx)
			return nil, ctx.Err()
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	acquireContext, cancelAcquire := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := registry.AcquireWithPolicy(acquireContext, lifecycleKey(1), AcquisitionPolicy{})
		result <- err
	}()

	waitForSignal(t, started)
	cancelAcquire()
	requireErrorIs(t, <-result, context.Canceled)
	requireErrorIs(t, <-startCanceled, errStartUnused)
	waitForRegistryEmpty(t, registry)
}

func TestRegistryStartDeadlineCannotStrandStartingState(t *testing.T) {
	clock := newFakeClock()
	started := make(chan context.Context, 1)
	returnFromStart := make(chan struct{})
	factory := &fakeFactory{
		startFn: func(ctx context.Context, _ Key, _ uint64) (Instance, error) {
			started <- ctx
			<-returnFromStart
			return nil, context.Cause(ctx)
		},
	}
	options := lifecycleOptions(clock)
	options.StartTimeout = 10 * time.Second
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	result := make(chan error, 1)
	go func() {
		_, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
		result <- err
	}()

	generationContext := <-started
	clock.Advance(options.StartTimeout)

	requireErrorIs(t, <-result, context.DeadlineExceeded)
	requireErrorIs(t, context.Cause(generationContext), context.DeadlineExceeded)
	status := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateFailed
	})
	if status.OpeningLeases != 0 {
		t.Fatalf("opening leases after start deadline = %d, want 0", status.OpeningLeases)
	}

	close(returnFromStart)
}

func TestRegistryDoesNotPublishReadyInstanceAtStartDeadline(t *testing.T) {
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
	options := lifecycleOptions(clock)
	options.StartTimeout = 10 * time.Second
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	result := make(chan error, 1)
	go func() {
		_, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
		result <- err
	}()

	waitForSignal(t, started)
	clock.AdvanceWithoutCallbacks(options.StartTimeout)
	close(returnReady)

	requireErrorIs(t, <-result, context.DeadlineExceeded)
	waitForSignal(t, instance.Done())
	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateFailed
	})
	if stops, kills := instance.Counts(); stops != 1 || kills != 0 {
		t.Fatalf("deadline-racing instance stop/kill counts = %d/%d, want 1/0", stops, kills)
	}

	clock.Advance(0)
}

func TestRegistryTracksLateLiveInstanceUntilStartupCleanupReapsIt(t *testing.T) {
	clock := newFakeClock()
	started := make(chan struct{})
	returnLateInstance := make(chan struct{})
	lateInstance := newFakeInstance()
	var attempt int
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			attempt++
			if attempt == 1 {
				close(started)
				<-returnLateInstance
				return lateInstance, nil
			}
			return newFakeInstance(), nil
		},
	}
	options := lifecycleOptions(clock)
	options.StartTimeout = 10 * time.Second
	options.FailureRetention = 10 * time.Second
	options.Jitter = func(delay time.Duration) time.Duration {
		return delay
	}
	registry, err := NewRegistry(factory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	firstResult := make(chan error, 1)
	go func() {
		_, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
		firstResult <- err
	}()

	waitForSignal(t, started)
	clock.Advance(options.StartTimeout)
	requireErrorIs(t, <-firstResult, context.DeadlineExceeded)

	clock.Advance(options.BackoffInitial + options.FailureRetention)
	if _, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{}); !errors.Is(err, ErrResourceUnavailable) {
		t.Fatalf("Acquire while timed-out startup is pending = %v, want ErrResourceUnavailable", err)
	}
	status := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateFailed
	})
	if status.Generation == 0 {
		t.Fatal("pending failed startup lost its generation")
	}
	if starts := factory.StartCount(); starts != 1 {
		t.Fatalf("Factory.Start count while cleanup pending = %d, want 1", starts)
	}

	close(returnLateInstance)
	waitForSignal(t, lateInstance.Done())
	waitForRegistryEmpty(t, registry)
	stops, kills := lateInstance.Counts()
	if stops != 1 || kills != 0 {
		t.Fatalf("late instance stop/kill counts = %d/%d, want 1/0", stops, kills)
	}

	replacement, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if starts := factory.StartCount(); starts != 2 {
		t.Fatalf("Factory.Start count after cleanup = %d, want 2", starts)
	}
	replacement.Release()
}
