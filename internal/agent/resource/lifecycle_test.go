package resource

// Provides deterministic factory, process, and clock fixtures for lifecycle
// transition tests.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeFactory struct {
	mu      sync.Mutex
	starts  int
	startFn func(context.Context, Key, uint64) (Instance, error)
}

var _ Factory = (*fakeFactory)(nil)

func (f *fakeFactory) Start(ctx context.Context, key Key, generation uint64) (Instance, error) {
	f.mu.Lock()
	f.starts++
	startFn := f.startFn
	f.mu.Unlock()

	if startFn != nil {
		return startFn(ctx, key, generation)
	}
	return newFakeInstance(), nil
}

func (f *fakeFactory) StartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.starts
}

type fakeInstance struct {
	done chan struct{}
	once sync.Once

	mu     sync.Mutex
	err    error
	stops  int
	kills  int
	stopFn func(context.Context) error
	killFn func(context.Context) error
}

var _ Instance = (*fakeInstance)(nil)

func newFakeInstance() *fakeInstance {
	return &fakeInstance{done: make(chan struct{})}
}

func (i *fakeInstance) Done() <-chan struct{} {
	return i.done
}

func (i *fakeInstance) Err() error {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.err
}

func (i *fakeInstance) Stop(ctx context.Context) error {
	i.mu.Lock()
	i.stops++
	stopFn := i.stopFn
	i.mu.Unlock()

	if stopFn != nil {
		return stopFn(ctx)
	}
	i.Exit(nil)
	return nil
}

func (i *fakeInstance) Kill(ctx context.Context) error {
	i.mu.Lock()
	i.kills++
	killFn := i.killFn
	i.mu.Unlock()

	if killFn != nil {
		return killFn(ctx)
	}
	i.Exit(nil)
	return nil
}

func (i *fakeInstance) Exit(err error) {
	i.once.Do(func() {
		i.mu.Lock()
		i.err = err
		i.mu.Unlock()
		close(i.done)
	})
}

func (i *fakeInstance) Counts() (int, int) {
	i.mu.Lock()
	defer i.mu.Unlock()

	return i.stops, i.kills
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*fakeTimer]struct{}
}

var _ Clock = (*fakeClock)(nil)

type fakeTimer struct {
	clock    *fakeClock
	deadline time.Time
	callback func()
	active   bool
}

var _ Timer = (*fakeTimer)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:    time.Unix(1_700_000_000, 0),
		timers: make(map[*fakeTimer]struct{}),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) AfterFunc(delay time.Duration, callback func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()

	timer := &fakeTimer{
		clock:    c,
		deadline: c.now.Add(delay),
		callback: callback,
		active:   true,
	}
	c.timers[timer] = struct{}{}
	return timer
}

func (c *fakeClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(elapsed)
	var callbacks []func()
	for timer := range c.timers {
		if timer.active && !timer.deadline.After(c.now) {
			timer.active = false
			delete(c.timers, timer)
			callbacks = append(callbacks, timer.callback)
		}
	}
	c.mu.Unlock()

	for _, callback := range callbacks {
		callback()
	}
}

func (c *fakeClock) AdvanceWithoutCallbacks(elapsed time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(elapsed)
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()

	if !t.active {
		return false
	}
	t.active = false
	delete(t.clock.timers, t)
	return true
}

func lifecycleKey(seed byte) Key {
	var digest [32]byte
	digest[0] = seed
	return Key{
		digest:    digest,
		kind:      KindMCPHTTP,
		transport: TransportHTTP,
		scope:     ScopeUser,
	}
}

func lifecycleOptions(clock Clock) Options {
	return Options{
		IdleTimeout:      10 * time.Second,
		StartTimeout:     time.Minute,
		StopGrace:        time.Second,
		KillGrace:        time.Second,
		BackoffInitial:   4 * time.Second,
		BackoffMaximum:   32 * time.Second,
		FailureRetention: time.Minute,
		StableReady:      20 * time.Second,
		Clock:            clock,
	}
}

func shutdownRegistry(t *testing.T, registry *Registry) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := registry.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func waitForStatus(t *testing.T, registry *Registry, check func(resourceStatus) bool) resourceStatus {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		statuses := registry.status()
		if len(statuses) == 1 && check(statuses[0]) {
			return statuses[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("status did not reach expected state: %+v", statuses)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForKeyStatus(t *testing.T, registry *Registry, key Summary, check func(resourceStatus) bool) resourceStatus {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		statuses := registry.status()
		for _, status := range statuses {
			if status.Key == key && check(status) {
				return status
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("resource %s did not reach expected state: %+v", key.ID, statuses)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForRegistryEmpty(t *testing.T, registry *Registry) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if statuses := registry.status(); len(statuses) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("resource registry did not become empty: %+v", registry.status())
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func requireErrorIs(t *testing.T, err, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(_, %v)", err, target)
	}
}
