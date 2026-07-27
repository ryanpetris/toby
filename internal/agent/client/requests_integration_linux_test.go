//go:build linux

package client

// Exercises independent gRPC requests and byte streams over the Unix transport.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/config"
)

func TestRequestStreamsOperateIndependently(t *testing.T) {
	session, coordinator, logs, stop := startRequestTestAgentService(t, nil)
	defer stop()
	defer session.Close()

	lease, err := session.Acquire(
		t.Context(),
		protocol.ResourceModels,
		json.RawMessage(`{"url":"https://models.invalid"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(t.Context())

	idle, err := session.OpenResourceStream(
		t.Context(),
		protocol.ResourceModels,
		lease,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()

	active, err := session.OpenResourceStream(
		t.Context(),
		protocol.ResourceModels,
		lease,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat(
		[]byte("independent stream"),
		protocol.MaxStreamDataBytes/len("independent stream")+2,
	)
	if _, err := active.Write(payload); err != nil {
		t.Fatal(err)
	}
	halfCloser, ok := active.(interface{ CloseWrite() error })
	if !ok {
		t.Fatal("resource stream does not support write half-close")
	}
	if err := halfCloser.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(active, echo); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(echo, payload) {
		t.Fatalf("echo = %q, want %q", echo, payload)
	}
	if _, err := active.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read after server completion = %v, want EOF", err)
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}

	models, err := session.ListModels(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 ||
		models[0].ModelID != "alpha" ||
		models[1].ModelID != "zeta" {
		t.Fatalf("models = %#v", models)
	}
	if err := session.FlushModelsCache(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if coordinator.flushes() != 1 {
		t.Fatalf(
			"models cache flushes = %d, want 1",
			coordinator.flushes(),
		)
	}

	resources, err := session.Resources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 1 ||
		resources[0].ResourceID != lease.resourceID ||
		resources[0].ActiveLeases != 1 {
		t.Fatalf("resources = %#v", resources)
	}

	operationID := protocol.OperationID("test-operation")
	file, err := logs.Create(
		protocol.ResourceModels,
		lease.resourceID,
		operationID,
	)
	if err != nil {
		t.Fatal(err)
	}
	const logText = "models generation output\n"
	if _, err := file.WriteString(logText); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	selected, err := session.ReadResourceLog(
		t.Context(),
		protocol.ResourceModels,
		lease.resourceID,
		operationID,
		&output,
	)
	if err != nil {
		t.Fatal(err)
	}
	if selected != operationID || output.String() != logText {
		t.Fatalf(
			"log = (%q, %q), want (%q, %q)",
			selected,
			output.String(),
			operationID,
			logText,
		)
	}
}

func TestCanceledResourceStreamDoesNotCloseAgentSession(t *testing.T) {
	session, _, _, stop := startRequestTestAgentService(t, nil)
	defer stop()
	defer session.Close()

	lease, err := session.Acquire(
		t.Context(),
		protocol.ResourceModels,
		json.RawMessage(`{"url":"https://models.invalid"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(t.Context())

	streamCtx, cancelStream := context.WithCancel(t.Context())
	stream, err := session.OpenResourceStream(
		streamCtx,
		protocol.ResourceModels,
		lease,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelStream()

	if _, err := stream.Read(make([]byte, 1)); err == nil {
		t.Fatal("canceled resource stream read succeeded")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-session.Done():
		t.Fatal("resource stream cancellation closed the agent session")
	default:
	}
	if _, err := session.Status(t.Context()); err != nil {
		t.Fatalf("status after resource stream cancellation: %v", err)
	}
}

func TestHostActionReturnsOverClientOpenedSession(t *testing.T) {
	requests := make(chan json.RawMessage, 1)
	handler := requestTestHostActionHandler(
		func(
			_ context.Context,
			request json.RawMessage,
		) (json.RawMessage, error) {
			requests <- append(json.RawMessage(nil), request...)
			return json.RawMessage(`{"result":"approved"}`), nil
		},
	)
	session, coordinator, _, stop := startRequestTestAgentService(t, handler)
	defer stop()
	defer session.Close()

	lease, err := session.Acquire(
		t.Context(),
		protocol.ResourceModels,
		json.RawMessage(`{"url":"https://models.invalid"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(t.Context())

	caller := coordinator.hostCaller()
	if caller == nil {
		t.Fatal("resource acquisition did not retain a host action caller")
	}
	request := json.RawMessage(`{"method":"git.commit"}`)
	response, err := caller.Call(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(response, []byte(`{"result":"approved"}`)) {
		t.Fatalf("host action response = %s", response)
	}
	select {
	case received := <-requests:
		if !bytes.Equal(received, request) {
			t.Fatalf("host action request = %s, want %s", received, request)
		}
	default:
		t.Fatal("launch handler did not receive the host action")
	}
}

func TestAgentServiceShutdownNoticeCarriesGraceAndWaitsForAcknowledgement(
	t *testing.T,
) {
	session, _, _, stop := startRequestTestAgentService(t, nil)
	stopDone := make(chan struct{})
	go func() {
		stop()
		close(stopDone)
	}()

	var notice ServiceStopping
	select {
	case notice = <-session.Stopping():
	case <-time.After(time.Second):
		t.Fatal("client did not receive agent shutdown notice")
	}
	if notice.GracePeriod != 17*time.Second {
		t.Fatalf(
			"shutdown grace = %s, want 17s",
			notice.GracePeriod,
		)
	}
	select {
	case <-stopDone:
		t.Fatal("agent stopped before client acknowledgement")
	default:
	}
	if _, err := session.Status(t.Context()); err == nil {
		t.Fatal("client accepted a new request while agent was stopping")
	}

	if err := session.AcknowledgeStopping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("agent did not stop after acknowledgement")
	}
}

func startRequestTestAgentService(
	t *testing.T,
	handler HostActionHandler,
) (
	*AgentSession,
	*requestTestCoordinator,
	*resourcelog.Service,
	func(),
) {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, "runtime", "agent.sock")
	election, err := socket.Elect(t.Context(), path, socket.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("test did not win agent election")
	}

	logs := resourcelog.NewService(config.Paths{
		Home:         root,
		XDGCacheHome: filepath.Join(root, "cache"),
	}, nil)
	coordinator := newRequestTestCoordinator()
	service, err := server.New(
		"test-version",
		coordinator,
		server.Options{ResourceLogs: logs},
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- service.Serve(
			ctx,
			election.Listener,
			server.ServeOptions{Persistent: true},
		)
	}()

	clients, err := NewService(
		path,
		"test-version",
		nil,
		ServiceOptions{},
	)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	session, err := clients.Connect(t.Context(), handler)
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve agent: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("agent did not stop")
		}
	}

	return session, coordinator, logs, stop
}

type requestTestCoordinator struct {
	mu         sync.Mutex
	next       uint64
	leases     map[protocol.LeaseID]*requestTestLease
	cacheFlush int
	caller     server.HostActionCaller
}

func newRequestTestCoordinator() *requestTestCoordinator {
	return &requestTestCoordinator{
		leases: make(map[protocol.LeaseID]*requestTestLease),
	}
}

func (c *requestTestCoordinator) AcquireResource(
	_ context.Context,
	request protocol.ResourceAcquireRequest,
	caller server.HostActionCaller,
) (server.ResourceLease, error) {
	if request.Kind != protocol.ResourceModels {
		return nil, fmt.Errorf("unexpected resource kind %q", request.Kind)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	lease := &requestTestLease{
		coordinator: c,
		resourceID:  "models-resource",
		leaseID: protocol.LeaseID(
			fmt.Sprintf("lease-%d", c.next),
		),
	}
	c.leases[lease.leaseID] = lease
	c.caller = caller

	return lease, nil
}

func (c *requestTestCoordinator) OpenResource(
	_ context.Context,
	kind protocol.ResourceKind,
	resourceID protocol.ResourceID,
	leaseID protocol.LeaseID,
) (server.ResourceStream, error) {
	c.mu.Lock()
	lease := c.leases[leaseID]
	c.mu.Unlock()
	if kind != protocol.ResourceModels ||
		lease == nil ||
		lease.resourceID != resourceID {
		return nil, fmt.Errorf("resource stream is not authorized")
	}

	return requestEchoStream{}, nil
}

func (c *requestTestCoordinator) ResourceSnapshot() server.ResourceSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	resources := uint64(0)
	if len(c.leases) > 0 {
		resources = 1
	}
	return server.ResourceSnapshot{
		ActiveResources: resources,
		ActiveLeases:    uint64(len(c.leases)),
	}
}

func (c *requestTestCoordinator) ListModels(
	_ context.Context,
	leaseID protocol.LeaseID,
) (map[string]any, error) {
	c.mu.Lock()
	lease := c.leases[leaseID]
	c.mu.Unlock()
	if lease == nil {
		return nil, fmt.Errorf("models lease is unavailable")
	}

	return map[string]any{
		"zeta":  map[string]any{"name": "Zeta"},
		"alpha": map[string]any{"name": "Alpha"},
	}, nil
}

func (c *requestTestCoordinator) FlushModelsCache(
	leaseID protocol.LeaseID,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.leases[leaseID] == nil {
		return fmt.Errorf("models lease is unavailable")
	}
	c.cacheFlush++

	return nil
}

func (c *requestTestCoordinator) ResourceItems() []server.ResourceItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.leases) == 0 {
		return nil
	}

	return []server.ResourceItem{{
		ID:           "models-resource",
		Kind:         protocol.ResourceModels,
		ActiveLeases: uint64(len(c.leases)),
	}}
}

func (c *requestTestCoordinator) flushes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cacheFlush
}

func (c *requestTestCoordinator) hostCaller() server.HostActionCaller {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.caller
}

type requestTestHostActionHandler func(
	context.Context,
	json.RawMessage,
) (json.RawMessage, error)

func (h requestTestHostActionHandler) Handle(
	ctx context.Context,
	request json.RawMessage,
) (json.RawMessage, error) {
	return h(ctx, request)
}

type requestTestLease struct {
	coordinator *requestTestCoordinator
	resourceID  protocol.ResourceID
	leaseID     protocol.LeaseID
	once        sync.Once
}

func (l *requestTestLease) ResourceID() protocol.ResourceID {
	return l.resourceID
}

func (l *requestTestLease) LeaseID() protocol.LeaseID {
	return l.leaseID
}

func (l *requestTestLease) Release(context.Context) error {
	l.once.Do(func() {
		l.coordinator.mu.Lock()
		delete(l.coordinator.leases, l.leaseID)
		l.coordinator.mu.Unlock()
	})

	return nil
}

type requestEchoStream struct{}

func (requestEchoStream) Serve(
	ctx context.Context,
	connection net.Conn,
) error {
	stop := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stop()

	_, err := io.Copy(connection, connection)
	return err
}

func (requestEchoStream) Close() error {
	return nil
}
