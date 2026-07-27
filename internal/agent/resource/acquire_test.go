package resource

// Verifies startup singleflight, readiness waiting, and generation-safe
// acquisition across stopping transitions.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRegistryConcurrentAcquiresJoinReadyStart(t *testing.T) {
	clock := newFakeClock()
	started := make(chan struct{})
	continueStart := make(chan struct{})
	instance := newFakeInstance()
	factory := &fakeFactory{
		startFn: func(ctx context.Context, _ Key, _ uint64) (Instance, error) {
			select {
			case <-started:
			default:
				close(started)
			}
			select {
			case <-continueStart:
				return instance, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	const callers = 100
	leases := make(chan *Lease, callers)
	errorsFound := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			lease, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
			if err != nil {
				errorsFound <- err
				return
			}
			leases <- lease
		}()
	}

	waitForSignal(t, started)
	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateStarting && status.OpeningLeases == callers
	})
	if got := factory.StartCount(); got != 1 {
		t.Fatalf("Factory.Start count = %d, want 1", got)
	}

	close(continueStart)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("Acquire: %v", err)
	}

	close(leases)
	var generation uint64
	var acquired int
	for lease := range leases {
		acquired++
		if generation == 0 {
			generation = lease.Generation()
		} else if lease.Generation() != generation {
			t.Fatalf("lease generation = %d, want %d", lease.Generation(), generation)
		}
		lease.Release()
	}
	if acquired != callers {
		t.Fatalf("acquired leases = %d, want %d", acquired, callers)
	}
}

func TestRegistryDoesNotHoldGlobalLockAcrossStart(t *testing.T) {
	clock := newFakeClock()
	blockedKey := lifecycleKey(1)
	started := make(chan struct{})
	continueStart := make(chan struct{})
	factory := &fakeFactory{
		startFn: func(ctx context.Context, key Key, _ uint64) (Instance, error) {
			if key != blockedKey {
				return newFakeInstance(), nil
			}
			close(started)
			select {
			case <-continueStart:
				return newFakeInstance(), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	firstResult := make(chan error, 1)
	go func() {
		lease, err := registry.AcquireWithPolicy(t.Context(), blockedKey, AcquisitionPolicy{})
		if lease != nil {
			lease.Release()
		}
		firstResult <- err
	}()
	waitForSignal(t, started)

	second, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(2), AcquisitionPolicy{})
	if err != nil {
		t.Fatalf("Acquire other key while start blocked: %v", err)
	}
	second.Release()

	close(continueStart)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
}

func TestRegistryAcquireDuringStoppingUsesReplacementGeneration(t *testing.T) {
	clock := newFakeClock()
	stopStarted := make(chan struct{})
	continueStop := make(chan struct{})
	var mu sync.Mutex
	var instances []*fakeInstance
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			instance := newFakeInstance()
			mu.Lock()
			instances = append(instances, instance)
			index := len(instances)
			mu.Unlock()
			if index == 1 {
				instance.stopFn = func(ctx context.Context) error {
					close(stopStarted)
					select {
					case <-continueStop:
						instance.Exit(nil)
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			}
			return instance, nil
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	first, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	firstGeneration := first.Generation()
	first.Release()

	clock.Advance(10 * time.Second)
	waitForSignal(t, stopStarted)

	result := make(chan *Lease, 1)
	acquireError := make(chan error, 1)
	go func() {
		lease, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
		if err != nil {
			acquireError <- err
			return
		}
		result <- lease
	}()
	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateStopping && status.OpeningLeases == 1
	})

	other, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(2), AcquisitionPolicy{})
	if err != nil {
		t.Fatalf("Acquire other key while stop blocked: %v", err)
	}
	other.Release()

	close(continueStop)
	var second *Lease
	select {
	case err := <-acquireError:
		t.Fatal(err)
	case second = <-result:
	}
	if second.Generation() <= firstGeneration {
		t.Fatalf("replacement generation = %d, want > %d", second.Generation(), firstGeneration)
	}

	registry.generationExited(lifecycleKey(1), firstGeneration, errors.New("stale exit"))
	status := waitForKeyStatus(t, registry, lifecycleKey(1).Summary(), func(status resourceStatus) bool {
		return status.State == StateReady
	})
	if status.Generation != second.Generation() {
		t.Fatalf("stale exit changed generation to %d, want %d", status.Generation, second.Generation())
	}
	second.Release()
}
