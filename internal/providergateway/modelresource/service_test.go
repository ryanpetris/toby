package modelresource

// Exercises dormant registration, coalesced model discovery, and cache
// generation invalidation.

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/config"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/providergateway"
)

func TestListModelsCoalescesAndCachesDynamicDiscovery(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeModelsBackend{
		started: started,
		release: release,
		models: map[string]any{
			"model-a": map[string]any{"name": "Model A"},
		},
	}
	resolver := &fakeModelsResolver{backend: backend}
	service := newModelService(t, resolver)
	resource := dynamicModelsResource()
	service.LeaseAcquired(resource)

	results := make(chan map[string]any, 2)
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			models, err := service.ListModels(
				t.Context(),
				resourcelease.StreamRequest{Resource: resource},
			)
			results <- models
			errors <- err
		}()
	}
	<-started
	close(release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if models := <-results; len(models) != 1 {
			t.Fatalf("models = %#v", models)
		}
	}

	models, err := service.ListModels(
		t.Context(),
		resourcelease.StreamRequest{Resource: resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("cached models = %#v", models)
	}
	if resolver.calls() != 1 {
		t.Fatalf("backend acquisitions = %d, want 1", resolver.calls())
	}
	if backend.calls() != 1 {
		t.Fatalf("discoveries = %d, want 1", backend.calls())
	}
}

func TestFlushPreventsInflightDiscoveryFromRepopulatingCache(
	t *testing.T,
) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := &fakeModelsBackend{
		started: started,
		release: release,
		models:  map[string]any{"model-a": map[string]any{}},
	}
	service := newModelService(
		t,
		&fakeModelsResolver{backend: backend},
	)
	resource := dynamicModelsResource()
	service.LeaseAcquired(resource)

	done := make(chan error, 1)
	go func() {
		_, err := service.ListModels(
			t.Context(),
			resourcelease.StreamRequest{Resource: resource},
		)
		done <- err
	}()
	<-started
	service.FlushModelsCache(resource)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	if _, err := service.ListModels(
		t.Context(),
		resourcelease.StreamRequest{Resource: resource},
	); err != nil {
		t.Fatal(err)
	}
	if backend.calls() != 2 {
		t.Fatalf("discoveries = %d, want 2", backend.calls())
	}
}

func TestWarmIdleExpiresWhileLeaseRemainsRegistered(t *testing.T) {
	backend := &fakeModelsBackend{}
	resolver := &fakeModelsResolver{backend: backend}
	service := newModelServiceWithOptions(t, resolver, Options{
		IdleTimeout:    5 * time.Millisecond,
		CleanupTimeout: time.Second,
		CacheTTL:       time.Hour,
		Jitter:         func(delay time.Duration) time.Duration { return delay },
	})
	resource := dynamicModelsResource()
	service.LeaseAcquired(resource)

	first, err := service.Open(
		t.Context(),
		resourcelease.StreamRequest{Resource: resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	for backend.releaseCalls() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := backend.releaseCalls(); got != 1 {
		t.Fatalf("idle backend releases = %d, want 1", got)
	}

	second, err := service.Open(
		t.Context(),
		resourcelease.StreamRequest{Resource: resource},
	)
	if err != nil {
		t.Fatalf("reopen after idle expiry: %v", err)
	}
	if got := resolver.calls(); got != 2 {
		t.Fatalf("backend acquisitions = %d, want 2", got)
	}
	_ = second.Close()
	service.LeaseReleased(resource)
}

func TestFirstUseRetriesNilBackend(t *testing.T) {
	backend := &fakeModelsBackend{}
	resolver := &fakeModelsResolver{
		backend:     backend,
		nilBackends: 1,
	}
	service := newModelServiceWithOptions(t, resolver, Options{
		IdleTimeout:    time.Hour,
		CleanupTimeout: time.Second,
		InitialRetry:   time.Millisecond,
		MaximumRetry:   2 * time.Millisecond,
		CacheTTL:       time.Hour,
		Jitter:         func(delay time.Duration) time.Duration { return delay },
	})
	resource := dynamicModelsResource()
	service.LeaseAcquired(resource)

	stream, err := service.Open(
		t.Context(),
		resourcelease.StreamRequest{Resource: resource},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolver.calls(); got != 2 {
		t.Fatalf("backend acquisitions = %d, want 2", got)
	}
	_ = stream.Close()
	service.LeaseReleased(resource)
}

func TestShutdownClosesResolver(t *testing.T) {
	resolver := &fakeModelsResolver{
		backend: &fakeModelsBackend{},
	}
	service := newModelService(t, resolver)

	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if resolver.shutdownCalls() != 1 {
		t.Fatalf(
			"resolver shutdown calls = %d, want 1",
			resolver.shutdownCalls(),
		)
	}
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resolver.shutdownCalls() != 1 {
		t.Fatalf(
			"resolver shutdown calls after repeat = %d, want 1",
			resolver.shutdownCalls(),
		)
	}
}

func newModelService(
	t *testing.T,
	resolver modelsResolver,
) *Service {
	t.Helper()
	return newModelServiceWithOptions(t, resolver, Options{
		IdleTimeout:    time.Hour,
		CleanupTimeout: time.Second,
		CacheTTL:       time.Hour,
		Jitter:         func(delay time.Duration) time.Duration { return delay },
	})
}

func newModelServiceWithOptions(
	t *testing.T,
	resolver modelsResolver,
	options Options,
) *Service {
	t.Helper()
	root := t.TempDir()
	logs := resourcelog.NewService(config.Paths{
		Home:         root,
		XDGCacheHome: filepath.Join(root, "cache"),
	}, nil)
	service, err := newService(resolver, logs, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return service
}

func dynamicModelsResource() resourcelease.Resolved {
	return resourcelease.Resolved{
		ID:   "models-test",
		Kind: protocol.ResourceModels,
		Configuration: modelsconfig.Config{
			Protocol: modelsconfig.ProtocolOpenAI,
			URL:      "https://models.example/v1",
		},
	}
}

type fakeModelsResolver struct {
	mu          sync.Mutex
	acquire     int
	shutdown    int
	backend     modelsBackend
	nilBackends int
}

func (r *fakeModelsResolver) Acquire(
	context.Context,
	modelsconfig.Config,
	providergateway.ProgressReporter,
) (modelsBackend, error) {
	r.mu.Lock()
	r.acquire++
	acquire := r.acquire
	nilBackends := r.nilBackends
	r.mu.Unlock()
	if acquire <= nilBackends {
		return nil, nil
	}
	return r.backend, nil
}

func (r *fakeModelsResolver) Shutdown(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shutdown++
	return nil
}

func (r *fakeModelsResolver) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acquire
}

func (r *fakeModelsResolver) shutdownCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shutdown
}

type fakeModelsBackend struct {
	mu       sync.Mutex
	discover int
	releases int
	started  chan struct{}
	release  <-chan struct{}
	models   map[string]any
}

func (b *fakeModelsBackend) Discover(
	ctx context.Context,
) (map[string]any, error) {
	b.mu.Lock()
	b.discover++
	started := b.started
	b.started = nil
	release := b.release
	b.release = nil
	b.mu.Unlock()
	if started != nil {
		close(started)
	}
	if release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
		}
	}
	return b.models, nil
}

func (*fakeModelsBackend) Serve(context.Context, net.Conn) error {
	return io.EOF
}

func (*fakeModelsBackend) Revoke() {}

func (b *fakeModelsBackend) Release(context.Context) error {
	b.mu.Lock()
	b.releases++
	b.mu.Unlock()
	return nil
}

func (b *fakeModelsBackend) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.discover
}

func (b *fakeModelsBackend) releaseCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.releases
}
