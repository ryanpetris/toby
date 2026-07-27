//go:build linux

package client

// Verifies bounded autostart, in-process launch singleflight, secure socket use,
// and protocol-compatible agent reuse across binary versions.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/diagnostic/warning"
)

type launcherFunc func(context.Context) error

func (f launcherFunc) Launch(ctx context.Context) error {
	return f(ctx)
}

func TestServiceAutostartsOnceForConcurrentCallers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toby", "agent.sock")
	coordinator := &clientTestCoordinator{}
	agentContext, agentCancel := context.WithCancel(t.Context())
	defer agentCancel()

	var launches atomic.Uint64
	started := make(chan struct{})
	serveResult := make(chan error, 1)
	launcher := launcherFunc(func(context.Context) error {
		launches.Add(1)
		go func() {
			time.Sleep(20 * time.Millisecond)

			election, err := socket.Elect(
				agentContext,
				path,
				socket.Options{},
			)
			if err != nil {
				serveResult <- err
				return
			}
			if election.Conn != nil {
				_ = election.Conn.Close()
				serveResult <- nil
				return
			}

			service, err := server.New(
				"test-version",
				coordinator,
				server.Options{},
			)
			if err != nil {
				_ = election.Listener.Close()
				serveResult <- err
				return
			}
			close(started)
			serveResult <- service.Serve(
				agentContext,
				election.Listener,
				server.ServeOptions{Persistent: true},
			)
		}()
		return nil
	})
	service, err := NewService(path, "test-version", launcher, ServiceOptions{
		StartupTimeout: 2 * time.Second,
		RetryMinimum:   time.Millisecond,
		RetryMaximum:   10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 12
	start := make(chan struct{})
	results := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start

			session, err := service.Connect(
				t.Context(),
				nil,
			)
			if err == nil {
				err = session.Close()
			}
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)

	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := launches.Load(); got != 1 {
		t.Fatalf("agent launches = %d, want 1", got)
	}
	select {
	case <-started:
	default:
		t.Fatal("agent server did not start")
	}

	status, err := service.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != protocol.ServiceReady {
		t.Fatalf("agent state = %q, want ready", status.State)
	}

	agentCancel()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not stop")
	}
}

func TestServiceStatusDoesNotAutostart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toby", "agent.sock")
	var launches atomic.Uint64
	service, err := NewService(
		path,
		"test-version",
		launcherFunc(func(context.Context) error {
			launches.Add(1)
			return nil
		}),
		ServiceOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Status(t.Context()); err == nil {
		t.Fatal("Status succeeded without an agent")
	}
	if got := launches.Load(); got != 0 {
		t.Fatalf("Status launched agent %d times", got)
	}
}

func TestServiceStopDoesNotAutostart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toby", "agent.sock")
	var launches atomic.Uint64
	service, err := NewService(
		path,
		"test-version",
		launcherFunc(func(context.Context) error {
			launches.Add(1)
			return nil
		}),
		ServiceOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.Stop(t.Context()); err == nil {
		t.Fatal("Stop succeeded without an agent")
	}
	if got := launches.Load(); got != 0 {
		t.Fatalf("Stop launched agent %d times", got)
	}
}

func TestServiceAcceptsDifferentBinaryVersionForSameProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "toby", "agent.sock")
	election, err := socket.Elect(t.Context(), path, socket.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("test did not win agent election")
	}
	serverService, err := server.New(
		"old-version",
		&clientTestCoordinator{},
		server.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- serverService.Serve(
			ctx,
			election.Listener,
			server.ServeOptions{Persistent: true},
		)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("old agent did not stop")
		}
	})

	var launches atomic.Uint64
	warningLogger := &clientWarningLogger{}
	suppression := warning.Suppression{}
	clientService, err := NewService(
		path,
		"new-version",
		launcherFunc(func(context.Context) error {
			launches.Add(1)
			return nil
		}),
		ServiceOptions{
			Warnings: warning.NewService(
				warningLogger,
				func() warning.Suppression {
					return suppression
				},
			),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	session, err := clientService.Connect(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := launches.Load(); got != 0 {
		t.Fatalf("compatible agent triggered %d launches", got)
	}
	for _, value := range []string{
		"agent.binary-version-mismatch",
		`client binary version "new-version"`,
		`agent binary version "old-version"`,
		"agent protocol version 1",
	} {
		if !strings.Contains(strings.Join(warningLogger.messages, "\n"), value) {
			t.Fatalf(
				"warning %q does not contain %q",
				warningLogger.messages,
				value,
			)
		}
	}

	warningLogger.messages = nil
	suppression = warning.Suppression{
		IDs: map[warning.ID]bool{
			warning.AgentBinaryMismatch: true,
		},
	}
	suppressed, err := clientService.Connect(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer suppressed.Close()
	if len(warningLogger.messages) != 0 {
		t.Fatalf("suppressed warning wrote %q", warningLogger.messages)
	}
}

type clientWarningLogger struct {
	messages []string
}

func (l *clientWarningLogger) Warn(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

func (l *clientWarningLogger) WarnError(
	message string,
	_ error,
	_ ...any,
) {
	l.messages = append(l.messages, message)
}

func (l *clientWarningLogger) Error(message string, _ ...any) {
	l.messages = append(l.messages, message)
}

type clientTestCoordinator struct{}

func (*clientTestCoordinator) AcquireResource(
	context.Context,
	protocol.ResourceAcquireRequest,
	server.HostActionCaller,
) (server.ResourceLease, error) {
	return nil, errors.New("resource acquisition is unavailable")
}

func (*clientTestCoordinator) OpenResource(
	context.Context,
	protocol.ResourceKind,
	protocol.ResourceID,
	protocol.LeaseID,
) (server.ResourceStream, error) {
	return nil, errors.New("resource streams are unavailable")
}

func (*clientTestCoordinator) ResourceSnapshot() server.ResourceSnapshot {
	return server.ResourceSnapshot{}
}
