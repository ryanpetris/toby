package resource

// Verifies reconciliation between asynchronous exit observation and bounded
// stop outcomes.

import "testing"

func TestStopCompletionHonorsWatcherObservedReap(t *testing.T) {
	clock := newFakeClock()
	registry, err := NewRegistry(&fakeFactory{}, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	key := lifecycleKey(1)
	instance := newFakeInstance()
	registry.mu.Lock()
	current, err := registry.entryWithIdleLocked(
		key,
		registry.options.IdleTimeout,
	)
	if err != nil {
		registry.mu.Unlock()
		t.Fatal(err)
	}
	current.generation = 1
	current.state = StateStopping
	current.instance = instance
	registry.mu.Unlock()

	instance.Exit(nil)
	registry.generationExited(key, 1, nil)
	registry.completeStop(key, 1, instance.Done(), stopOutcome{timedOut: true})

	registry.mu.Lock()
	state := current.state
	lastError := current.lastError
	_, retained := registry.entries[key]
	registry.mu.Unlock()

	if state != StateCold {
		t.Fatalf("state = %q, want %q", state, StateCold)
	}
	if lastError != "" {
		t.Fatalf("LastError = %q after watcher-confirmed reap", lastError)
	}
	if retained {
		t.Fatal("quiescent cold entry remained in registry")
	}
}

func TestStopCompletionRechecksDoneBeforeCommittingTimeout(t *testing.T) {
	clock := newFakeClock()
	registry, err := NewRegistry(&fakeFactory{}, lifecycleOptions(clock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownRegistry(t, registry) })

	key := lifecycleKey(1)
	instance := newFakeInstance()
	registry.mu.Lock()
	current, err := registry.entryWithIdleLocked(
		key,
		registry.options.IdleTimeout,
	)
	if err != nil {
		registry.mu.Unlock()
		t.Fatal(err)
	}
	current.generation = 1
	current.state = StateStopping
	current.instance = instance
	registry.mu.Unlock()

	instance.Exit(nil)
	registry.completeStop(key, 1, instance.Done(), stopOutcome{timedOut: true})
	registry.generationExited(key, 1, nil)

	registry.mu.Lock()
	state := current.state
	lastError := current.lastError
	_, retained := registry.entries[key]
	registry.mu.Unlock()

	if state != StateCold {
		t.Fatalf("state = %q, want %q", state, StateCold)
	}
	if lastError != "" {
		t.Fatalf("LastError = %q after authoritative Done recheck", lastError)
	}
	if retained {
		t.Fatal("quiescent cold entry remained in registry")
	}
}
