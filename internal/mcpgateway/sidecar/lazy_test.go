package sidecar

// Verifies lazy singleflight, retry, typed-nil rejection, and close behavior
// without opening a native filesystem or Bubblewrap facility.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"petris.dev/toby/internal/mcpgateway"
)

func TestLazyRuntimeInitializesOnceForConcurrentCalls(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	runtime := &fakeRuntime{}
	lazy, err := newLazy(func(context.Context) (Runtime, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return runtime, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lazy.Close()

	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, callErr := lazy.Resolve(
				t.Context(),
				testDefinition(),
				nil,
			)
			errorsSeen <- callErr
		}()
	}
	<-started
	close(release)
	wait.Wait()
	close(errorsSeen)

	for callErr := range errorsSeen {
		if callErr != nil {
			t.Fatal(callErr)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("runtime factory calls = %d, want 1", got)
	}
}

func TestLazyRuntimeRetriesFailedInitialization(t *testing.T) {
	var calls atomic.Int32
	runtime := &fakeRuntime{}
	lazy, err := newLazy(func(context.Context) (Runtime, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("temporary failure")
		}
		return runtime, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lazy.Close()

	if _, err := lazy.Resolve(
		t.Context(),
		testDefinition(),
		nil,
	); err == nil {
		t.Fatal("first initialization unexpectedly succeeded")
	}
	if _, err := lazy.Resolve(
		t.Context(),
		testDefinition(),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("runtime factory calls = %d, want 2", got)
	}
}

func TestLazyRuntimeCloseDoesNotInitializeAndRefusesFutureCalls(
	t *testing.T,
) {
	var calls atomic.Int32
	lazy, err := newLazy(func(context.Context) (Runtime, error) {
		calls.Add(1)
		return &fakeRuntime{}, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := lazy.Close(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("Close initialized runtime %d times", got)
	}
	if _, err := lazy.Resolve(
		t.Context(),
		testDefinition(),
		nil,
	); err == nil {
		t.Fatal("closed lazy runtime accepted an operation")
	}
}

func TestLazyRuntimeRejectsTypedNilFactoryResult(t *testing.T) {
	var runtime *fakeRuntime
	lazy, err := newLazy(func(context.Context) (Runtime, error) {
		return runtime, nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lazy.Close()

	if _, err := lazy.Resolve(
		t.Context(),
		testDefinition(),
		nil,
	); err == nil {
		t.Fatal("typed-nil runtime was accepted")
	}
}

type fakeRuntime struct {
	closeCalls atomic.Int32
}

var _ Runtime = (*fakeRuntime)(nil)

func (*fakeRuntime) Resolve(
	context.Context,
	Definition,
	mcpgateway.ProgressReporter,
) (Metadata, error) {
	return Metadata{Workdir: "/"}, nil
}

func (*fakeRuntime) Prepare(
	context.Context,
	Definition,
	mcpgateway.ProgressReporter,
) (*Prepared, error) {
	return nil, errors.New("unused")
}

func (*fakeRuntime) PinMounts(
	context.Context,
	[]mcpgateway.Mount,
) (*MountCapabilities, error) {
	return nil, errors.New("unused")
}

func (*fakeRuntime) PreparePinned(
	context.Context,
	Definition,
	*MountCapabilities,
	mcpgateway.ProgressReporter,
) (*Prepared, error) {
	return nil, errors.New("unused")
}

func (r *fakeRuntime) Close() error {
	r.closeCalls.Add(1)
	return nil
}

func testDefinition() Definition {
	return Definition{
		Image:   "example.invalid/mcp:latest",
		Command: []string{"/bin/mcp"},
	}
}
