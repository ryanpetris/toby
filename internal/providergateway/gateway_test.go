package providergateway

// Exercises transactional route publication, immediate authorization
// revocation, rejected-load rollback, and restart replay.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

type testPool struct {
	mu          sync.Mutex
	generations []*testGeneration
	acquires    int
	shutdown    bool
}

var _ Pool = (*testPool)(nil)

func (p *testPool) Acquire(
	ctx context.Context,
	_ ProgressReporter,
) (Generation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.shutdown {
		return nil, fmt.Errorf("test pool is shut down")
	}
	if p.acquires >= len(p.generations) {
		return nil, fmt.Errorf("test generation sequence exhausted")
	}
	generation := p.generations[p.acquires]
	p.acquires++

	return generation, nil
}

func (p *testPool) Shutdown(context.Context) error {
	p.mu.Lock()
	p.shutdown = true
	p.mu.Unlock()

	return nil
}

type testGeneration struct {
	id uint64

	mu        sync.Mutex
	loads     [][]byte
	loadError []error
	released  bool
	done      chan struct{}
	doneOnce  sync.Once
}

var _ Generation = (*testGeneration)(nil)

func newTestGeneration(id uint64) *testGeneration {
	return &testGeneration{
		id:   id,
		done: make(chan struct{}),
	}
}

func (g *testGeneration) Generation() uint64 {
	return g.id
}

func (g *testGeneration) DialData(
	context.Context,
) (*net.UnixConn, error) {
	return nil, fmt.Errorf("test generation data dial was unexpected")
}

func (g *testGeneration) Load(
	ctx context.Context,
	config []byte,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.loads = append(g.loads, append([]byte(nil), config...))
	if len(g.loadError) == 0 {
		return nil
	}
	err := g.loadError[0]
	g.loadError = g.loadError[1:]

	return err
}

func (g *testGeneration) OpenConnector() (Connector, error) {
	return newTestRelayConnector(), nil
}

func (g *testGeneration) Done() <-chan struct{} {
	return g.done
}

func (g *testGeneration) Err() error {
	return nil
}

func (g *testGeneration) Release() {
	g.mu.Lock()
	g.released = true
	g.mu.Unlock()
	g.doneOnce.Do(func() {
		close(g.done)
	})
}

func (g *testGeneration) exit() {
	g.doneOnce.Do(func() {
		close(g.done)
	})
}

func (g *testGeneration) loadCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return len(g.loads)
}

type testDiscoverer struct{}

var _ ModelDiscoverer = (*testDiscoverer)(nil)

func (*testDiscoverer) Discover(
	_ context.Context,
	_ ProviderDescriptor,
) (map[string]any, error) {
	return map[string]any{}, nil
}

func TestServicePublishesAndImmediatelyRevokesAuthorization(
	t *testing.T,
) {
	generation := newTestGeneration(1)
	service := newTestGateway(
		t,
		&testPool{generations: []*testGeneration{generation}},
		&testDiscoverer{},
	)

	acquisition, err := service.acquire(
		testContext(t),
		RequestSpec{Providers: []ProviderSpec{{
			ID:   "primary",
			Type: ProviderOpenAI,
			Name: "Primary",
			URL:  "https://provider.invalid/v1",
			Headers: map[string]string{
				"Authorization": "Bearer real-secret",
			},
		}}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	config := acquisition.descriptor.clone()
	if len(config.Providers) != 1 ||
		config.Providers[0].Credential == "" {
		t.Fatalf("descriptor = %#v", config)
	}
	if generation.loadCount() != 1 {
		t.Fatalf(
			"initial Caddy loads = %d, want 1",
			generation.loadCount(),
		)
	}

	route := storedTestRoute(t, service, acquisition.routeIDs[0])
	request, err := http.NewRequest(
		http.MethodGet,
		"http://auth.invalid"+route.authPath(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	service.routes.mu.RLock()
	token := service.routes.generationToken
	service.routes.mu.RUnlock()
	request.Header.Set(internalGatewayTokenHeader, token)
	request.Header.Set(internalCapabilityHeader, route.Capability)
	credentialName, credential := route.credentialHeader()
	request.Header.Set(credentialName, credential)
	if !service.routes.authorize(request, func() {}) {
		t.Fatal("active route was not authorized")
	}

	acquisition.Revoke()
	if service.routes.authorize(request, func() {}) {
		t.Fatal("revoked route remained authorized")
	}
	if err := acquisition.Release(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if generation.loadCount() != 2 {
		t.Fatalf(
			"Caddy loads after removal = %d, want 2",
			generation.loadCount(),
		)
	}
}

func TestServiceRollsBackRejectedInitialConfiguration(
	t *testing.T,
) {
	generation := newTestGeneration(1)
	generation.loadError = []error{ErrConfigurationRejected}
	service := newTestGateway(
		t,
		&testPool{generations: []*testGeneration{generation}},
		&testDiscoverer{},
	)

	_, err := service.acquire(
		testContext(t),
		RequestSpec{Providers: []ProviderSpec{{
			ID:   "rejected",
			Type: ProviderOpenAI,
			Name: "Rejected",
			URL:  "https://provider.invalid",
		}}},
		nil,
	)
	if err == nil {
		t.Fatal("rejected configuration acquisition succeeded")
	}

	waitForTest(t, func() bool {
		return generation.loadCount() >= 2 &&
			len(service.routes.snapshot().Routes) == 0
	})
}

func TestServiceReplaysCurrentRoutesAfterGenerationExit(
	t *testing.T,
) {
	first := newTestGeneration(1)
	second := newTestGeneration(2)
	service := newTestGateway(
		t,
		&testPool{
			generations: []*testGeneration{first, second},
		},
		&testDiscoverer{},
	)

	acquisition, err := service.acquire(
		testContext(t),
		RequestSpec{Providers: []ProviderSpec{{
			ID:   "primary",
			Type: ProviderOpenAI,
			Name: "Primary",
			URL:  "https://provider.invalid",
		}}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	first.exit()
	waitForTest(t, func() bool {
		return second.loadCount() == 1
	})
	if len(service.routes.snapshot().Routes) != 1 {
		t.Fatal("generation replay lost the desired route")
	}

	if err := acquisition.Release(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestServiceShutdownWithActiveRouteDoesNotWaitForCanceledReconcile(
	t *testing.T,
) {
	generation := newTestGeneration(1)
	service := newTestGateway(
		t,
		&testPool{generations: []*testGeneration{generation}},
		&testDiscoverer{},
	)

	acquisition, err := service.acquire(
		testContext(t),
		RequestSpec{Providers: []ProviderSpec{{
			ID:   "primary",
			Type: ProviderOpenAI,
			Name: "Primary",
			URL:  "https://provider.invalid",
		}}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Shutdown(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if !acquisition.revoked {
		t.Fatal("active acquisition was not revoked")
	}
	if len(service.routes.snapshot().Routes) != 0 {
		t.Fatal("shutdown retained a desired provider route")
	}
}

func newTestGateway(
	t *testing.T,
	pool Pool,
	discoverer ModelDiscoverer,
) *Gateway {
	t.Helper()

	var tokenMu sync.Mutex
	tokenIndex := 0
	socketDirectory := t.TempDir()
	if err := os.Chmod(socketDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := NewGateway(
		testContext(t),
		socketDirectory+"/auth.sock",
		pool,
		discoverer,
		Options{
			CleanupTimeout: 2 * time.Second,
			RetryDelay:     time.Millisecond,
			NewToken: func() (string, error) {
				tokenMu.Lock()
				defer tokenMu.Unlock()
				tokenIndex++
				return fmt.Sprintf(
					"test-token-%d",
					tokenIndex,
				), nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			2*time.Second,
		)
		defer cancel()
		if err := service.Shutdown(ctx); err != nil &&
			!errors.Is(err, context.Canceled) {
			t.Errorf("Shutdown() error = %v", err)
		}
	})

	return service
}

func testContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(
		context.Background(),
		2*time.Second,
	)
	t.Cleanup(cancel)

	return ctx
}

func storedTestRoute(
	t *testing.T,
	service *Gateway,
	id string,
) route {
	t.Helper()

	service.routes.mu.RLock()
	defer service.routes.mu.RUnlock()
	stored := service.routes.routes[id]
	if stored == nil {
		t.Fatalf("route %q is missing", id)
	}

	return stored.route.clone()
}

func waitForTest(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
