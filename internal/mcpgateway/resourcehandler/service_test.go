package resourcehandler

// Exercises lazy MCP activation, shared startup, retry bounds, and lease
// demand cleanup without launching external sidecars.

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/config"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/config/mcpresource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/connector"
)

type connectorTargetFunc func(context.Context, io.ReadWriteCloser)

func (f connectorTargetFunc) ServeConnector(
	ctx context.Context,
	connection io.ReadWriteCloser,
) {
	f(ctx, connection)
}

func TestStreamServesTransportIndependentConnection(t *testing.T) {
	const resourceID = protocol.ResourceID("resource")

	service := &Service{
		runtimes: map[protocol.ResourceID]*runtimeState{
			resourceID: {
				resource: resourcelease.Resolved{ID: resourceID},
				streams:  1,
			},
		},
	}
	acquired := &fakeAcquired{
		target: connectorTargetFunc(func(
			_ context.Context,
			connection io.ReadWriteCloser,
		) {
			payload := make([]byte, len("request"))
			if _, err := io.ReadFull(connection, payload); err != nil {
				return
			}
			_, _ = connection.Write(append([]byte("reply:"), payload...))
		}),
	}
	stream, err := newStream(service, resourceID, acquired)
	if err != nil {
		t.Fatal(err)
	}

	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	serveDone := make(chan error, 1)
	go func() {
		defer serverConnection.Close()
		serveDone <- stream.Serve(t.Context(), serverConnection)
	}()

	if _, err := clientConnection.Write([]byte("request")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len("reply:request"))
	if _, err := io.ReadFull(clientConnection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "reply:request" {
		t.Fatalf("response = %q, want %q", response, "reply:request")
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRegistrationDoesNotStartBackend(t *testing.T) {
	t.Parallel()

	service, resolvers := newTestService(t, Options{})
	resource := testResource()

	service.LeaseAcquired(resource)
	for class, resolver := range resolvers {
		if got := resolver.acquireCount(); got != 0 {
			t.Fatalf("%s acquire count = %d, want 0", class, got)
		}
	}

	service.LeaseReleased(resource)
	service.mu.Lock()
	count := len(service.runtimes)
	service.mu.Unlock()
	if count != 0 {
		t.Fatalf("retained runtime count = %d, want 0", count)
	}
}

func TestConcurrentFirstUseStartsOneBackend(t *testing.T) {
	t.Parallel()

	service, resolvers := newTestService(t, Options{})
	resource := testResource()
	service.LeaseAcquired(resource)
	service.LeaseAcquired(resource)

	remote := resolvers[mcpgateway.TargetRemoteHTTP]
	remote.acquireStarted = make(chan struct{})
	remote.acquireContinue = make(chan struct{})

	results := make(chan streamResult, 2)
	for range 2 {
		go func() {
			stream, err := service.Open(t.Context(), resourcelease.StreamRequest{
				Resource: resource,
			})
			results <- streamResult{stream: stream, err: err}
		}()
	}

	select {
	case <-remote.acquireStarted:
	case <-time.After(time.Second):
		t.Fatal("backend acquisition did not start")
	}
	if got := remote.acquireCount(); got != 1 {
		t.Fatalf("acquire count before release = %d, want 1", got)
	}
	close(remote.acquireContinue)

	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("Open() error = %v", result.err)
		}
		if err := result.stream.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
	if got := remote.acquireCount(); got != 1 {
		t.Fatalf("final acquire count = %d, want 1", got)
	}

	service.LeaseReleased(resource)
	service.LeaseReleased(resource)
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFirstUseRetriesFailedStartup(t *testing.T) {
	t.Parallel()

	service, resolvers := newTestService(t, Options{
		InitialRetry: time.Millisecond,
		MaximumRetry: 2 * time.Millisecond,
		Jitter:       identityJitter,
	})
	resource := testResource()
	service.LeaseAcquired(resource)
	resolvers[mcpgateway.TargetRemoteHTTP].failures = 1

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stream, err := service.Open(ctx, resourcelease.StreamRequest{
		Resource: resource,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got := resolvers[mcpgateway.TargetRemoteHTTP].acquireCount(); got != 2 {
		t.Fatalf("acquire count = %d, want 2", got)
	}
	_ = stream.Close()

	service.LeaseReleased(resource)
	if err := service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestFirstUseFailsFastOnPermanentStartup(t *testing.T) {
	t.Parallel()

	service, resolvers := newTestService(t, Options{
		InitialRetry: time.Millisecond,
		MaximumRetry: 2 * time.Millisecond,
		Jitter:       identityJitter,
	})
	resource := testResource()
	service.LeaseAcquired(resource)
	remote := resolvers[mcpgateway.TargetRemoteHTTP]
	remote.failures = 8
	remote.permanent = true

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := service.Open(ctx, resourcelease.StreamRequest{
		Resource: resource,
	})
	if err == nil || !strings.Contains(err.Error(), "planned acquisition failure") {
		t.Fatalf("Open() error = %v", err)
	}
	if got := remote.acquireCount(); got != 1 {
		t.Fatalf("acquire count = %d, want 1", got)
	}

	service.LeaseReleased(resource)
}

func TestWarmIdleExpiresWhileLeaseRemainsRegistered(t *testing.T) {
	t.Parallel()

	service, resolvers := newTestService(t, Options{
		IdleTimeout:    5 * time.Millisecond,
		CleanupTimeout: time.Second,
	})
	resource := testResource()
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

	remote := resolvers[mcpgateway.TargetRemoteHTTP]
	deadline := time.Now().Add(time.Second)
	for remote.releaseCount() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := remote.releaseCount(); got != 1 {
		t.Fatalf("idle backend releases = %d, want 1", got)
	}

	second, err := service.Open(
		t.Context(),
		resourcelease.StreamRequest{Resource: resource},
	)
	if err != nil {
		t.Fatalf("reopen after idle expiry: %v", err)
	}
	if got := remote.acquireCount(); got != 2 {
		t.Fatalf("backend acquisitions = %d, want 2", got)
	}
	_ = second.Close()
	service.LeaseReleased(resource)
}

func TestRetryDelayIsCappedAfterJitter(t *testing.T) {
	t.Parallel()

	service, _ := newTestService(t, Options{
		InitialRetry: time.Second,
		MaximumRetry: 5 * time.Minute,
		Jitter: func(delay time.Duration) time.Duration {
			return delay * 2
		},
	})

	if got := service.retryDelay(64); got != 5*time.Minute {
		t.Fatalf("retryDelay(64) = %s, want 5m", got)
	}
}

func identityJitter(delay time.Duration) time.Duration {
	return delay
}

type streamResult struct {
	stream interface {
		Close() error
	}
	err error
}

func testResource() resourcelease.Resolved {
	return resourcelease.Resolved{
		ID:   protocol.ResourceID("resource-id"),
		Kind: protocol.ResourceMCP,
		Configuration: mcpresource.Config{
			Type: mcpresource.TypeConfigured,
			Server: &mcpconfig.Server{
				Type:      mcpconfig.ServerRemote,
				Transport: mcpconfig.TransportHTTP,
				URL:       "https://mcp.example.invalid/",
			},
		},
	}
}

func newTestService(
	t *testing.T,
	options Options,
) (*Service, map[mcpgateway.TargetClass]*fakeResolver) {
	t.Helper()

	resolvers := map[mcpgateway.TargetClass]*fakeResolver{
		mcpgateway.TargetBuiltin: {
			class: mcpgateway.TargetBuiltin,
		},
		mcpgateway.TargetLocalHTTP: {
			class: mcpgateway.TargetLocalHTTP,
		},
		mcpgateway.TargetLocalStdio: {
			class: mcpgateway.TargetLocalStdio,
		},
		mcpgateway.TargetRemoteHTTP: {
			class: mcpgateway.TargetRemoteHTTP,
		},
	}
	backends := make([]mcpgateway.BackendResolver, 0, len(resolvers))
	for _, class := range []mcpgateway.TargetClass{
		mcpgateway.TargetBuiltin,
		mcpgateway.TargetLocalHTTP,
		mcpgateway.TargetLocalStdio,
		mcpgateway.TargetRemoteHTTP,
	} {
		backends = append(backends, resolvers[class])
	}

	service, err := New(
		backends,
		resourcelog.NewService(config.Paths{
			Home:         t.TempDir(),
			XDGCacheHome: t.TempDir(),
		}, nil),
		options,
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := service.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return service, resolvers
}

type fakeResolver struct {
	class mcpgateway.TargetClass

	mu              sync.Mutex
	acquires        int
	releases        int
	failures        int
	permanent       bool
	acquireStarted  chan struct{}
	acquireContinue chan struct{}
}

var _ mcpgateway.BackendResolver = (*fakeResolver)(nil)

func (r *fakeResolver) Class() mcpgateway.TargetClass {
	return r.class
}

func (r *fakeResolver) Resolve(
	context.Context,
	mcpgateway.TargetRequest,
) (mcpgateway.PreparedBackend, error) {
	return fakePrepared{resolver: r}, nil
}

func (*fakeResolver) Shutdown(context.Context) error {
	return nil
}

func (r *fakeResolver) acquireCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acquires
}

func (r *fakeResolver) releaseCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.releases
}

type fakePrepared struct {
	resolver *fakeResolver
}

func (p fakePrepared) Acquire(
	ctx context.Context,
	_ mcpgateway.ProgressReporter,
) (mcpgateway.AcquiredBackend, error) {
	p.resolver.mu.Lock()
	p.resolver.acquires++
	acquireNumber := p.resolver.acquires
	failures := p.resolver.failures
	permanent := p.resolver.permanent
	started := p.resolver.acquireStarted
	continueChannel := p.resolver.acquireContinue
	p.resolver.mu.Unlock()

	if started != nil && acquireNumber == 1 {
		close(started)
	}
	if continueChannel != nil && acquireNumber == 1 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-continueChannel:
		}
	}
	if acquireNumber <= failures {
		err := fmt.Errorf("planned acquisition failure")
		if permanent {
			err = mcpgateway.Permanent(err)
		}
		return nil, err
	}

	return &fakeAcquired{
		resolver: p.resolver,
		target: connectorTargetFunc(
			func(context.Context, io.ReadWriteCloser) {},
		),
	}, nil
}

type fakeAcquired struct {
	resolver *fakeResolver
	target   connector.Target
}

func (a *fakeAcquired) Target() connector.Target {
	return a.target
}

func (*fakeAcquired) Revoke() {}

func (a *fakeAcquired) Release(context.Context) error {
	a.resolver.mu.Lock()
	a.resolver.releases++
	a.resolver.mu.Unlock()
	return nil
}
