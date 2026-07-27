package server

// Exercises listener shutdown and rejects invalid construction.

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/protocol"
)

func TestServeStopsWhenContextIsCanceled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New("test-version", &testResourceCoordinator{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- service.Serve(
			ctx,
			listener,
			ServeOptions{Persistent: true},
		)
	}()

	deadline := time.Now().Add(time.Second)
	for service.serviceState() != "ready" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestServeWaitsForListenerCleanup(t *testing.T) {
	listener := &blockingCloseListener{
		acceptDone:   make(chan struct{}),
		closeStarted: make(chan struct{}),
		allowClose:   make(chan struct{}),
	}
	service, err := New("test-version", &testResourceCoordinator{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- service.Serve(
			ctx,
			listener,
			ServeOptions{Persistent: true},
		)
	}()

	deadline := time.Now().Add(time.Second)
	for service.serviceState() != "ready" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-listener.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("listener cleanup did not start")
	}
	select {
	case err := <-result:
		t.Fatalf("Serve returned before listener cleanup: %v", err)
	default:
	}

	close(listener.allowClose)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not return after listener cleanup")
	}
}

func TestServeStopsAfterObservedWorkBecomesIdle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	coordinator := &testResourceCoordinator{}
	coordinator.setSnapshot(ResourceSnapshot{ActiveResources: 1})
	service, err := New(
		"test-version",
		coordinator,
		Options{
			StartupGrace:      time.Hour,
			IdleCheckInterval: time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- service.Serve(t.Context(), listener, ServeOptions{})
	}()

	waitForServiceState(t, service, protocol.ServiceReady)
	session := &agentSession{
		id: protocol.SessionID("test-session"),
	}
	if err := service.registerSession(session); err != nil {
		t.Fatal(err)
	}
	service.unregisterSession(session)

	select {
	case err := <-result:
		t.Fatalf("Serve stopped while a resource remained active: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	coordinator.setSnapshot(ResourceSnapshot{})
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after all work became idle")
	}
}

func TestServeStopsAfterStartupGraceWithoutAClient(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(
		"test-version",
		&testResourceCoordinator{},
		Options{
			StartupGrace:      5 * time.Millisecond,
			IdleCheckInterval: time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- service.Serve(t.Context(), listener, ServeOptions{})
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop after its unused startup grace")
	}
}

func TestPersistentServeRemainsIdle(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(
		"test-version",
		&testResourceCoordinator{},
		Options{
			StartupGrace:      time.Millisecond,
			IdleCheckInterval: time.Millisecond,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- service.Serve(
			ctx,
			listener,
			ServeOptions{Persistent: true},
		)
	}()

	waitForServiceState(t, service, protocol.ServiceReady)
	select {
	case err := <-result:
		t.Fatalf("persistent Serve stopped while idle: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("persistent Serve did not stop after cancellation")
	}
}

func TestServiceRejectsInvalidConstruction(t *testing.T) {
	coordinator := &testResourceCoordinator{}
	if _, err := New("", coordinator, Options{}); err == nil {
		t.Fatal("New accepted an empty version")
	}
	if _, err := New("version", nil, Options{}); err == nil {
		t.Fatal("New accepted a nil coordinator")
	}

	service, err := New("version", coordinator, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var nilContext context.Context
	if err := service.Serve(
		nilContext,
		&testListener{},
		ServeOptions{},
	); err == nil {
		t.Fatal("Serve accepted a nil context")
	}
	if err := service.Serve(
		t.Context(),
		nil,
		ServeOptions{},
	); err == nil {
		t.Fatal("Serve accepted a nil listener")
	}
}

type testResourceCoordinator struct {
	mu       sync.Mutex
	snapshot ResourceSnapshot
}

func (*testResourceCoordinator) AcquireResource(
	context.Context,
	protocol.ResourceAcquireRequest,
	HostActionCaller,
) (ResourceLease, error) {
	return nil, errors.New("resource acquisition is unavailable")
}

func (*testResourceCoordinator) OpenResource(
	context.Context,
	protocol.ResourceKind,
	protocol.ResourceID,
	protocol.LeaseID,
) (ResourceStream, error) {
	return nil, errors.New("resource streams are unavailable")
}

func (c *testResourceCoordinator) ResourceSnapshot() ResourceSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.snapshot
}

func (c *testResourceCoordinator) setSnapshot(snapshot ResourceSnapshot) {
	c.mu.Lock()
	c.snapshot = snapshot
	c.mu.Unlock()
}

func waitForServiceState(
	t *testing.T,
	service *Service,
	state protocol.ServiceState,
) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for service.serviceState() != state {
		if time.Now().After(deadline) {
			t.Fatalf(
				"agent state = %q, want %q",
				service.serviceState(),
				state,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

type testListener struct{}

func (*testListener) Accept() (net.Conn, error) {
	return nil, errors.New("unused")
}

func (*testListener) Close() error {
	return nil
}

func (*testListener) Addr() net.Addr {
	return testAddr("test")
}

type testAddr string

func (a testAddr) Network() string {
	return string(a)
}

func (a testAddr) String() string {
	return string(a)
}

var _ net.Listener = (*testListener)(nil)
var _ net.Addr = testAddr("")

type blockingCloseListener struct {
	acceptDone   chan struct{}
	closeStarted chan struct{}
	allowClose   chan struct{}
	closeOnce    sync.Once
}

var _ net.Listener = (*blockingCloseListener)(nil)

func (l *blockingCloseListener) Accept() (net.Conn, error) {
	<-l.acceptDone
	return nil, net.ErrClosed
}

func (l *blockingCloseListener) Close() error {
	l.closeOnce.Do(func() {
		close(l.acceptDone)
		close(l.closeStarted)
		<-l.allowClose
	})
	return nil
}

func (*blockingCloseListener) Addr() net.Addr {
	return testAddr("blocking")
}
