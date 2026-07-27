package localstdio

// Exercises one-process-per-connector lifetime, definition isolation, and
// revocation-driven reaping.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
)

func TestTargetLaunchesOneProcessPerConnectorUntilRevoked(t *testing.T) {
	t.Parallel()

	launcher := &recordingLauncher{
		started:  make(chan string, 2),
		finished: make(chan struct{}, 2),
	}
	resolver, err := NewResolver(launcher, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			Name: "stdio",
			Spec: mcpgateway.TargetSpec{
				Type:      mcpgateway.TargetLocal,
				Transport: mcpgateway.TransportStdio,
				Image:     "example.invalid/mcp@sha256:aaaa",
				Command:   []string{"/bin/server"},
				Environment: map[string]string{
					"TOKEN": "original",
				},
				Scope:   resource.ScopeRun,
				Network: resource.NetworkHost,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if launcher.callCount() != 0 {
		t.Fatal("Resolve launched a stdio process")
	}
	if launcher.prepareCount() != 0 {
		t.Fatal("Resolve prepared a stdio target before acquisition")
	}
	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if launcher.prepareCount() != 1 {
		t.Fatalf(
			"Acquire prepare calls = %d, want 1",
			launcher.prepareCount(),
		)
	}
	if launcher.callCount() != 0 {
		t.Fatal("Acquire launched a stdio process before a connector")
	}

	for range 2 {
		go target.Target().ServeConnector(t.Context(), nil)
		select {
		case token := <-launcher.started:
			if token != "original" {
				t.Fatalf("launcher token = %q, want isolated original", token)
			}
		case <-time.After(time.Second):
			t.Fatal("connector did not launch its stdio process")
		}
	}
	if got := launcher.callCount(); got != 2 {
		t.Fatalf("launcher calls = %d, want 2", got)
	}
	target.Revoke()
	for range 2 {
		select {
		case <-launcher.finished:
		case <-time.After(time.Second):
			t.Fatal("stdio process did not finish after revocation")
		}
	}
	if err := target.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if launcher.closeCount() != 1 {
		t.Fatalf(
			"released prepared launch closes = %d, want 1",
			launcher.closeCount(),
		)
	}
}

func TestAcquireClosesPreparedLaunchWhenRegistrationFails(t *testing.T) {
	t.Parallel()

	launcher := &recordingLauncher{}
	resolver, err := NewResolver(launcher, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			Name: "stdio",
			Spec: mcpgateway.TargetSpec{
				Type:      mcpgateway.TargetLocal,
				Transport: mcpgateway.TransportStdio,
				Image:     "image",
				Command:   []string{"/bin/server"},
				Scope:     resource.ScopeRun,
				Network:   resource.NetworkHost,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	if _, err := prepared.Acquire(t.Context(), nil); err == nil {
		t.Fatal("Acquire succeeded after resolver shutdown")
	}
	if launcher.prepareCount() != 1 || launcher.closeCount() != 1 {
		t.Fatalf(
			"failed registration lifecycle = prepares %d, closes %d; want 1, 1",
			launcher.prepareCount(),
			launcher.closeCount(),
		)
	}
}

func TestResolverShutdownWaitsForActiveProcess(t *testing.T) {
	t.Parallel()

	launcher := &recordingLauncher{
		started:  make(chan string, 1),
		finished: make(chan struct{}, 1),
	}
	resolver, err := NewResolver(launcher, nil)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			Name: "stdio",
			Spec: mcpgateway.TargetSpec{
				Type:      mcpgateway.TargetLocal,
				Transport: mcpgateway.TransportStdio,
				Image:     "image",
				Command:   []string{"/bin/server"},
				Scope:     resource.ScopeRun,
				Network:   resource.NetworkHost,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	target, err := prepared.Acquire(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	go target.Target().ServeConnector(t.Context(), nil)
	<-launcher.started

	if err := resolver.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-launcher.finished:
	default:
		t.Fatal("Shutdown returned before the stdio process reaped")
	}
	if _, err := resolver.Resolve(
		t.Context(),
		mcpgateway.TargetRequest{
			Name: "stdio",
			Spec: mcpgateway.TargetSpec{
				Type:      mcpgateway.TargetLocal,
				Transport: mcpgateway.TransportStdio,
				Image:     "image",
				Command:   []string{"/bin/server"},
			},
		},
	); err == nil {
		t.Fatal("resolver accepted a target after shutdown")
	}
}

type recordingLauncher struct {
	mu       sync.Mutex
	prepares int
	calls    int
	closes   int
	started  chan string
	finished chan struct{}
}

var _ Launcher = (*recordingLauncher)(nil)

func (l *recordingLauncher) Prepare(
	_ context.Context,
	launch Launch,
	_ mcpgateway.ProgressReporter,
) (PreparedLaunch, error) {
	l.mu.Lock()
	l.prepares++
	l.mu.Unlock()

	return &recordingPrepared{
		owner:  l,
		launch: launch.clone(),
	}, nil
}

type recordingPrepared struct {
	owner  *recordingLauncher
	launch Launch
}

var _ PreparedLaunch = (*recordingPrepared)(nil)

func (p *recordingPrepared) Serve(
	ctx context.Context,
	_ io.ReadWriteCloser,
) error {
	token := p.launch.Environment["TOKEN"]

	p.owner.mu.Lock()
	p.owner.calls++
	p.owner.mu.Unlock()
	p.owner.started <- token

	<-ctx.Done()
	p.owner.finished <- struct{}{}
	return ctx.Err()
}

func (p *recordingPrepared) Close() error {
	p.owner.mu.Lock()
	p.owner.closes++
	p.owner.mu.Unlock()
	return nil
}

func (l *recordingLauncher) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

func (l *recordingLauncher) prepareCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.prepares
}

func (l *recordingLauncher) closeCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closes
}
