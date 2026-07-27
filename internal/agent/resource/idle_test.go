package resource

// Verifies generation-specific idle timer cancellation and restart.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistryAcquireDuringIdleCancelsOnlyCurrentTimer(t *testing.T) {
	clock := newFakeClock()
	instance := newFakeInstance()
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
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
	generation := first.Generation()
	first.Release()
	firstIdle := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateIdle
	})

	clock.Advance(6 * time.Second)
	second, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation() != generation {
		t.Fatalf("idle acquire generation = %d, want %d", second.Generation(), generation)
	}

	clock.Advance(10 * time.Second)
	status := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateReady
	})
	if !status.IdleDeadline.IsZero() {
		t.Fatalf("ready resource retained idle deadline %s", status.IdleDeadline)
	}
	stops, _ := instance.Counts()
	if stops != 0 {
		t.Fatalf("stale idle timer stopped active generation %d times", stops)
	}

	second.Release()
	secondIdle := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateIdle
	})
	if !secondIdle.IdleDeadline.After(firstIdle.IdleDeadline) {
		t.Fatalf("new idle deadline %s did not advance past %s", secondIdle.IdleDeadline, firstIdle.IdleDeadline)
	}

	clock.Advance(10 * time.Second)
	waitForRegistryEmpty(t, registry)
}

func TestRegistryUsesPerResourceIdlePolicy(t *testing.T) {
	clock := newFakeClock()
	registry, err := NewRegistry(
		&fakeFactory{},
		lifecycleOptions(clock),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	lease, err := registry.AcquireWithPolicy(
		t.Context(),
		lifecycleKey(1),
		AcquisitionPolicy{IdleTimeout: time.Minute},
	)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	status := waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateIdle
	})
	if got := status.IdleDeadline.Sub(clock.Now()); got != time.Minute {
		t.Fatalf("custom idle deadline = %s, want 1m", got)
	}

	clock.Advance(30 * time.Second)
	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateIdle
	})
	clock.Advance(30 * time.Second)
	waitForRegistryEmpty(t, registry)
}

func TestRegistryRejectsPolicyMismatchForSameKey(t *testing.T) {
	clock := newFakeClock()
	registry, err := NewRegistry(
		&fakeFactory{},
		lifecycleOptions(clock),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	lease, err := registry.AcquireWithPolicy(
		t.Context(),
		lifecycleKey(1),
		AcquisitionPolicy{IdleTimeout: time.Minute},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	if _, err := registry.AcquireWithPolicy(
		t.Context(),
		lifecycleKey(1),
		AcquisitionPolicy{IdleTimeout: 2 * time.Minute},
	); err == nil {
		t.Fatal("AcquireWithPolicy accepted a conflicting idle timeout")
	}
}

func TestLeaseAndConnectorReleaseAreIdempotent(t *testing.T) {
	clock := newFakeClock()
	registry, err := NewRegistry(&fakeFactory{}, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	lease, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	gotInstance, err := lease.Instance()
	if err != nil {
		t.Fatal(err)
	}
	if gotInstance == nil {
		t.Fatal("Lease.Instance returned nil")
	}
	connector, err := lease.OpenConnector()
	if err != nil {
		t.Fatal(err)
	}

	connector.Close()
	connector.Close()
	if err := connector.Err(); err != nil {
		t.Fatalf("Connector.Err after normal close = %v", err)
	}

	lease.Release()
	lease.Release()
	if err := lease.Err(); err != nil {
		t.Fatalf("Lease.Err after normal release = %v", err)
	}
	if state := lease.State(); state != LeaseClosed {
		t.Fatalf("Lease.State = %q, want %q", state, LeaseClosed)
	}
	if _, err := lease.Instance(); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("Instance after release = %v, want ErrLeaseClosed", err)
	}
	if _, err := lease.OpenConnector(); !errors.Is(err, ErrLeaseClosed) {
		t.Fatalf("OpenConnector after release = %v, want ErrLeaseClosed", err)
	}
}

func TestConnectorKeepsGenerationReadyAfterLeaseRelease(t *testing.T) {
	clock := newFakeClock()
	instance := newFakeInstance()
	factory := &fakeFactory{
		startFn: func(context.Context, Key, uint64) (Instance, error) {
			return instance, nil
		},
	}
	registry, err := NewRegistry(factory, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	lease, err := registry.AcquireWithPolicy(t.Context(), lifecycleKey(1), AcquisitionPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	connector, err := lease.OpenConnector()
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()

	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateReady && status.Leases == 0 && status.Connectors == 1
	})
	clock.Advance(20 * time.Second)
	stops, _ := instance.Counts()
	if stops != 0 {
		t.Fatalf("active connector allowed %d idle stops", stops)
	}

	connector.Close()
	waitForStatus(t, registry, func(status resourceStatus) bool {
		return status.State == StateIdle && status.Connectors == 0
	})
	clock.Advance(10 * time.Second)
	waitForRegistryEmpty(t, registry)
}
