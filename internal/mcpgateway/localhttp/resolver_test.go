package localhttp

// Exercises shared process leases, distinct logical connector sessions, and
// run-local revocation.

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
	"petris.dev/toby/internal/sandbox/layout"
)

func TestSharedProcessKeepsConnectorSessionsRunLocal(t *testing.T) {
	t.Parallel()

	pool := newFakePool()
	bridge := &recordingBridge{
		started:  make(chan string, 2),
		finished: make(chan string, 2),
	}
	resolver, err := NewResolver(pool, bridge, nil)
	if err != nil {
		t.Fatal(err)
	}

	first := acquireHTTPTestTarget(t, resolver)
	second := acquireHTTPTestTarget(t, resolver)
	if pool.acquireCalls() != 2 {
		t.Fatalf("process lease acquires = %d, want 2", pool.acquireCalls())
	}
	if pool.processStarts != 1 {
		t.Fatalf("shared process starts = %d, want 1", pool.processStarts)
	}

	go first.Target().ServeConnector(t.Context(), nil)
	firstID := <-bridge.started
	go second.Target().ServeConnector(t.Context(), nil)
	secondID := <-bridge.started
	if firstID == secondID {
		t.Fatalf("two runs received the same connector identity %q", firstID)
	}

	first.Revoke()
	select {
	case finished := <-bridge.finished:
		if finished != firstID {
			t.Fatalf("revoking first run ended session %q", finished)
		}
	case <-time.After(time.Second):
		t.Fatal("first run session did not end after revocation")
	}
	select {
	case finished := <-bridge.finished:
		t.Fatalf("revoking first run also ended session %q", finished)
	case <-time.After(20 * time.Millisecond):
	}

	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if pool.activeLeaseCount() != 1 {
		t.Fatalf(
			"active process leases after first release = %d, want 1",
			pool.activeLeaseCount(),
		)
	}

	second.Revoke()
	select {
	case finished := <-bridge.finished:
		if finished != secondID {
			t.Fatalf("revoking second run ended session %q", finished)
		}
	case <-time.After(time.Second):
		t.Fatal("second run session did not end after revocation")
	}
	if err := second.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestResolverDelegatesReadinessAndShutdown(t *testing.T) {
	t.Parallel()

	pool := newFakePool()
	bridge := &recordingBridge{
		started:  make(chan string, 1),
		finished: make(chan string, 1),
	}
	resolver, err := NewResolver(pool, bridge, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.Resolve(
		t.Context(),
		httpTestRequest(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if pool.acquireCalls() != 0 {
		t.Fatal("Resolve started the local HTTP process")
	}
	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if pool.acquireCalls() != 1 {
		t.Fatalf("Acquire readiness calls = %d, want 1", pool.acquireCalls())
	}
	if err := target.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := resolver.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !pool.shutdown {
		t.Fatal("resolver did not shut down its process pool")
	}
}

type recordingBridge struct {
	started  chan string
	finished chan string
}

var _ Bridge = (*recordingBridge)(nil)

func (b *recordingBridge) Serve(
	ctx context.Context,
	_ io.ReadWriteCloser,
	upstream httpbridge.Upstream,
) error {
	id := upstream.Headers.Get("X-Test-Lease")
	b.started <- id
	<-ctx.Done()
	b.finished <- id
	return ctx.Err()
}

type fakePool struct {
	mu sync.Mutex

	processStarts int
	acquires      int
	nextLease     int
	leases        map[*fakeServiceLease]struct{}
	shutdown      bool
}

var _ Pool = (*fakePool)(nil)

func newFakePool() *fakePool {
	return &fakePool{
		processStarts: 1,
		leases:        make(map[*fakeServiceLease]struct{}),
	}
}

func (p *fakePool) Prepare(
	context.Context,
	Definition,
	mcpgateway.ProgressReporter,
) (Preparation, error) {
	return &fakePreparation{pool: p}, nil
}

func (p *fakePool) Shutdown(context.Context) error {
	p.mu.Lock()
	p.shutdown = true
	p.mu.Unlock()
	return nil
}

func (p *fakePool) acquireCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.acquires
}

func (p *fakePool) activeLeaseCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.leases)
}

type fakePreparation struct {
	pool *fakePool
}

var _ Preparation = (*fakePreparation)(nil)

func (*fakePreparation) Close() error {
	return nil
}

func (p *fakePreparation) Acquire(
	context.Context,
) (ServiceLease, error) {
	p.pool.mu.Lock()
	defer p.pool.mu.Unlock()

	p.pool.acquires++
	p.pool.nextLease++
	lease := &fakeServiceLease{
		pool:       p.pool,
		id:         p.pool.nextLease,
		connectors: make(map[*fakeServiceConnector]struct{}),
	}
	p.pool.leases[lease] = struct{}{}
	return lease, nil
}

type fakeServiceLease struct {
	pool *fakePool
	id   int

	mu         sync.Mutex
	connectors map[*fakeServiceConnector]struct{}
	revoked    bool
}

var _ ServiceLease = (*fakeServiceLease)(nil)

func (l *fakeServiceLease) OpenConnector(
	context.Context,
) (ServiceConnector, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	connector := &fakeServiceConnector{
		lease: l,
		done:  make(chan struct{}),
	}
	l.connectors[connector] = struct{}{}
	return connector, nil
}

func (l *fakeServiceLease) Revoke() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.revoked {
		return
	}
	l.revoked = true
	for connector := range l.connectors {
		connector.closeLocked()
		delete(l.connectors, connector)
	}
}

func (l *fakeServiceLease) Release(context.Context) error {
	l.Revoke()
	l.pool.mu.Lock()
	delete(l.pool.leases, l)
	l.pool.mu.Unlock()
	return nil
}

type fakeServiceConnector struct {
	lease  *fakeServiceLease
	done   chan struct{}
	closed bool
}

var _ ServiceConnector = (*fakeServiceConnector)(nil)

func (c *fakeServiceConnector) Upstream() (httpbridge.Upstream, error) {
	return httpbridge.Upstream{
		Endpoint: "http://127.0.0.1/mcp",
		Headers: http.Header{
			"X-Test-Lease": []string{strconv.Itoa(c.lease.id)},
		},
	}, nil
}

func (c *fakeServiceConnector) Done() <-chan struct{} {
	return c.done
}

func (*fakeServiceConnector) Err() error {
	return nil
}

func (c *fakeServiceConnector) Close() {
	c.lease.mu.Lock()
	defer c.lease.mu.Unlock()

	delete(c.lease.connectors, c)
	c.closeLocked()
}

func (c *fakeServiceConnector) closeLocked() {
	if c.closed {
		return
	}
	c.closed = true
	close(c.done)
}

func acquireHTTPTestTarget(
	t *testing.T,
	resolver *Resolver,
) mcpgateway.AcquiredBackend {
	t.Helper()

	prepared, err := resolver.Resolve(t.Context(), httpTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func httpTestRequest() mcpgateway.TargetRequest {
	return mcpgateway.TargetRequest{
		Name: "http",
		Spec: mcpgateway.TargetSpec{
			Type:      mcpgateway.TargetLocal,
			Transport: mcpgateway.TransportHTTP,
			Image:     "image",
			Command:   []string{"/bin/server"},
			Endpoint: &mcpgateway.Endpoint{
				Kind:   mcpgateway.EndpointUnix,
				Socket: layout.Runtime + "/mcp.sock",
				Path:   "/mcp",
			},
			Scope:   resource.ScopeUser,
			Network: resource.NetworkHost,
		},
	}
}
