//go:build linux

package client

// Exercises typed OCI event delivery over the gRPC agent transport.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/agent/socket"
)

func TestOCIEventsRoundTripOverAgentStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime", "agent.sock")
	election, err := socket.Elect(t.Context(), path, socket.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if election.Listener == nil {
		t.Fatal("test did not win agent election")
	}

	coordinator := &ociTestCoordinator{}
	agentServer, err := server.New(
		"test-version",
		coordinator,
		server.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	agentContext, cancelAgentService := context.WithCancel(t.Context())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- agentServer.Serve(
			agentContext,
			election.Listener,
			server.ServeOptions{Persistent: true},
		)
	}()
	defer func() {
		cancelAgentService()
		select {
		case err := <-serveDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("agent did not stop")
		}
	}()

	clients, err := NewService(
		path,
		"test-version",
		nil,
		ServiceOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := clients.Connect(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	lease, err := session.Acquire(
		t.Context(),
		protocol.ResourceOCI,
		json.RawMessage(`{"reference":"example.invalid/test:latest"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release(t.Context())

	events, err := session.PrepareOCI(t.Context(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer events.Close()

	var received []protocol.OCIEvent
	for {
		event, err := events.Recv()
		if err != nil {
			if err != io.EOF {
				t.Fatal(err)
			}
			break
		}
		received = append(received, event)
	}
	if len(received) != 4 {
		t.Fatalf("received %d OCI events, want 4", len(received))
	}
	for index, kind := range []protocol.OCIEventKind{
		protocol.OCIEventAccepted,
		protocol.OCIEventSnapshot,
		protocol.OCIEventOutput,
		protocol.OCIEventComplete,
	} {
		if received[index].Kind != kind {
			t.Fatalf(
				"event %d kind = %q, want %q",
				index,
				received[index].Kind,
				kind,
			)
		}
	}
	if received[1].Progress == nil ||
		received[1].Progress.CompletedBytes != 512 ||
		received[1].Progress.TotalBytes != 1024 {
		t.Fatalf("snapshot progress = %#v", received[1].Progress)
	}
	if string(received[2].Data) != "registry output\n" ||
		received[2].Source != protocol.OCISourceRegistry ||
		received[2].Stream != protocol.OutputStderr {
		t.Fatalf("output event = %#v", received[2])
	}
	if !received[3].Cached {
		t.Fatal("complete event did not preserve cached state")
	}
}

type ociTestCoordinator struct {
	mu     sync.Mutex
	active bool
}

func (c *ociTestCoordinator) AcquireResource(
	_ context.Context,
	request protocol.ResourceAcquireRequest,
	_ server.HostActionCaller,
) (server.ResourceLease, error) {
	if request.Kind != protocol.ResourceOCI {
		return nil, errors.New("unexpected resource kind")
	}

	c.mu.Lock()
	c.active = true
	c.mu.Unlock()

	return &ociTestLease{coordinator: c}, nil
}

func (c *ociTestCoordinator) OpenResource(
	_ context.Context,
	kind protocol.ResourceKind,
	resourceID protocol.ResourceID,
	leaseID protocol.LeaseID,
) (server.ResourceStream, error) {
	if kind != protocol.ResourceOCI ||
		resourceID != "oci-resource" ||
		leaseID != "oci-lease" {
		return nil, errors.New("unexpected OCI stream identity")
	}

	return ociTestStream{}, nil
}

func (c *ociTestCoordinator) ResourceSnapshot() server.ResourceSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.active {
		return server.ResourceSnapshot{}
	}
	return server.ResourceSnapshot{
		ActiveResources: 1,
		ActiveLeases:    1,
	}
}

type ociTestLease struct {
	coordinator *ociTestCoordinator
	once        sync.Once
}

func (*ociTestLease) ResourceID() protocol.ResourceID {
	return "oci-resource"
}

func (*ociTestLease) LeaseID() protocol.LeaseID {
	return "oci-lease"
}

func (l *ociTestLease) Release(context.Context) error {
	l.once.Do(func() {
		l.coordinator.mu.Lock()
		l.coordinator.active = false
		l.coordinator.mu.Unlock()
	})

	return nil
}

type ociTestStream struct{}

func (ociTestStream) Follow(
	_ context.Context,
	send func(protocol.OCIEvent) error,
) error {
	operationID := protocol.OperationID("oci-operation")
	progress := protocol.OCIProgressState{
		Phase:          protocol.OCIProgressDownloading,
		CompletedBytes: 512,
		TotalBytes:     1024,
		CompletedItems: 1,
		TotalItems:     2,
	}
	for _, event := range []protocol.OCIEvent{
		{
			Kind:        protocol.OCIEventAccepted,
			OperationID: operationID,
		},
		{
			Kind:        protocol.OCIEventSnapshot,
			OperationID: operationID,
			Sequence:    1,
			Progress:    &progress,
		},
		{
			Kind:        protocol.OCIEventOutput,
			OperationID: operationID,
			Sequence:    2,
			Source:      protocol.OCISourceRegistry,
			Stream:      protocol.OutputStderr,
			Data:        []byte("registry output\n"),
		},
		{
			Kind:        protocol.OCIEventComplete,
			OperationID: operationID,
			Sequence:    3,
			Cached:      true,
		},
	} {
		if err := send(event); err != nil {
			return err
		}
	}

	return nil
}

func (ociTestStream) Close() error {
	return nil
}
