//go:build linux

package clientresource

// Exercises launch UUID translation against the agent protocol.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelease"
	"petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/agent/socket"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/config/mcpresource"
	modelsconfig "petris.dev/toby/internal/config/models"
	"petris.dev/toby/internal/resourcehash"
)

func TestLaunchUUIDsShareResource(t *testing.T) {
	resources := newIntegrationResources(t)
	agent, _, stop := startIntegrationAgentService(t, resources)
	defer stop()
	defer agent.Close()

	service, err := NewRegistry(protocol.ResourceMCP, agent, nil)
	if err != nil {
		t.Fatalf("new client resource registry: %v", err)
	}
	first, err := service.Acquire(
		t.Context(),
		integrationMCPConfig(t, map[string]string{"b": "2", "a": "1"}),
	)
	if err != nil {
		t.Fatalf("acquire first resource: %v", err)
	}
	second, err := service.Acquire(
		t.Context(),
		integrationMCPConfig(t, map[string]string{"a": "1", "b": "2"}),
	)
	if err != nil {
		t.Fatalf("acquire second resource: %v", err)
	}

	if first == second {
		t.Fatalf("independent launch resources reused UUID %q", first)
	}
	if got := resources.Snapshot(); got != (resourcelease.Snapshot{
		ActiveResources: 1,
		ActiveLeases:    2,
	}) {
		t.Fatalf("agent resource snapshot = %#v", got)
	}

	stream, err := service.Open(t.Context(), first)
	if err != nil {
		t.Fatalf("open translated resource stream: %v", err)
	}
	payload := []byte("bidirectional stream")
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("write resource stream: %v", err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, echo); err != nil {
		t.Fatalf("read resource stream: %v", err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("stream echo = %q, want %q", echo, payload)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("close resource stream: %v", err)
	}

	if err := service.Close(t.Context()); err != nil {
		t.Fatalf("close resource registry: %v", err)
	}
	if got := resources.Snapshot(); got != (resourcelease.Snapshot{}) {
		t.Fatalf("agent snapshot after close = %#v", got)
	}
}

func TestAgentSessionLossInvalidatesClientTranslations(t *testing.T) {
	resources := newIntegrationResources(t)
	agent, _, stop := startIntegrationAgentService(t, resources)
	defer stop()

	service, err := NewRegistry(protocol.ResourceMCP, agent, nil)
	if err != nil {
		t.Fatalf("new client resource registry: %v", err)
	}
	if _, err := service.Acquire(
		t.Context(),
		integrationMCPConfig(t, nil),
	); err != nil {
		t.Fatalf("acquire resource: %v", err)
	}
	if err := agent.Close(); err != nil {
		t.Fatalf("close agent session: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for resources.Snapshot().ActiveLeases != 0 &&
		time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := resources.Snapshot(); got != (resourcelease.Snapshot{}) {
		t.Fatalf("agent snapshot after session loss = %#v", got)
	}
}

func TestSeparateSessionsShareResourceWithIndependentLeases(
	t *testing.T,
) {
	resources := newIntegrationResources(t)
	firstAgent, clients, stop := startIntegrationAgentService(t, resources)
	defer stop()
	defer firstAgent.Close()

	secondAgent, err := clients.Connect(
		t.Context(),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer secondAgent.Close()

	firstService, err := NewRegistry(protocol.ResourceMCP, firstAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer firstService.Close(t.Context())
	secondService, err := NewRegistry(protocol.ResourceMCP, secondAgent, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer secondService.Close(t.Context())

	first, err := firstService.Acquire(
		t.Context(),
		integrationMCPConfig(t, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondService.Acquire(
		t.Context(),
		integrationMCPConfig(t, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("separate sessions reused client UUID %q", first)
	}
	if got := resources.Snapshot(); got != (resourcelease.Snapshot{
		ActiveResources: 1,
		ActiveLeases:    2,
	}) {
		t.Fatalf("shared agent snapshot = %#v", got)
	}

	if err := firstService.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	stream, err := secondService.Open(t.Context(), second)
	if err != nil {
		t.Fatalf("open second session after first release: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if got := resources.Snapshot(); got != (resourcelease.Snapshot{
		ActiveResources: 1,
		ActiveLeases:    1,
	}) {
		t.Fatalf("snapshot after first-session release = %#v", got)
	}
}

func TestModelsUUIDListsDiscoveredAgentModels(t *testing.T) {
	want := map[string]any{
		"alpha": map[string]any{"name": "Alpha"},
		"beta":  map[string]any{"name": "Beta"},
	}
	resources := newModelsIntegrationResources(t, want)
	agent, _, stop := startIntegrationAgentService(t, resources)
	defer stop()
	defer agent.Close()

	service, err := NewRegistry(protocol.ResourceModels, agent, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close(t.Context())

	id, err := service.Acquire(t.Context(), modelsconfig.Config{
		Protocol: modelsconfig.ProtocolOpenAI,
		URL:      "https://example.invalid/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListModels(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	got := make(map[string]any, len(items))
	for _, item := range items {
		var model any
		if err := json.Unmarshal(item.Model, &model); err != nil {
			t.Fatal(err)
		}
		got[item.ModelID] = model
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func newIntegrationResources(t *testing.T) *resourcelease.Service {
	t.Helper()

	resolver, err := resourcelease.NewMCPResolver(resourcehash.NewService())
	if err != nil {
		t.Fatalf("new canonical resolver: %v", err)
	}
	resources, err := resourcelease.NewService(
		[]resourcelease.Resolver{resolver},
		[]resourcelease.ResourceOpener{echoResourceOpener{}},
	)
	if err != nil {
		t.Fatalf("new agent resource service: %v", err)
	}
	t.Cleanup(func() {
		if err := resources.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return resources
}

func newModelsIntegrationResources(
	t *testing.T,
	models map[string]any,
) *resourcelease.Service {
	t.Helper()

	resolver, err := resourcelease.NewModelsResolver(
		resourcehash.NewService(),
	)
	if err != nil {
		t.Fatalf("new models resolver: %v", err)
	}
	resources, err := resourcelease.NewService(
		[]resourcelease.Resolver{resolver},
		[]resourcelease.ResourceOpener{
			modelsResourceOpener{models: models},
		},
	)
	if err != nil {
		t.Fatalf("new agent resource service: %v", err)
	}
	t.Cleanup(func() {
		if err := resources.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})

	return resources
}

func integrationMCPConfig(
	t *testing.T,
	headers map[string]string,
) mcpresource.Config {
	t.Helper()

	result, err := mcpresource.Configured(
		mcpconfig.Server{
			Type:      mcpconfig.ServerRemote,
			Transport: mcpconfig.TransportHTTP,
			URL:       "https://example.invalid",
			Headers:   headers,
		},
		mcpresource.ScopeIdentities{},
	)
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func startIntegrationAgentService(
	t *testing.T,
	resources *resourcelease.Service,
) (*agentclient.AgentSession, *agentclient.Service, func()) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "toby", "agent.sock")
	election, err := socket.Elect(t.Context(), path, socket.Options{})
	if err != nil {
		t.Fatalf("elect agent socket: %v", err)
	}
	if election.Listener == nil {
		t.Fatal("test did not win agent socket election")
	}
	service, err := server.New(
		"test-version",
		resources,
		server.Options{},
	)
	if err != nil {
		t.Fatalf("new agent server: %v", err)
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

	clientService, err := agentclient.NewService(
		path,
		"test-version",
		nil,
		agentclient.ServiceOptions{},
	)
	if err != nil {
		cancel()
		t.Fatalf("new agent client: %v", err)
	}
	agent, err := clientService.Connect(
		t.Context(),
		nil,
	)
	if err != nil {
		cancel()
		t.Fatalf("connect agent client: %v", err)
	}

	var once bool
	stop := func() {
		if once {
			return
		}
		once = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve agent: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("agent server did not stop")
		}
	}

	return agent, clientService, stop
}

type echoResourceOpener struct{}

func (echoResourceOpener) Kind() protocol.ResourceKind {
	return protocol.ResourceMCP
}

func (echoResourceOpener) Open(
	context.Context,
	resourcelease.StreamRequest,
) (server.ResourceStream, error) {
	return echoStream{}, nil
}

type echoStream struct{}

func (echoStream) Serve(ctx context.Context, connection net.Conn) error {
	stop := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	defer stop()

	_, err := io.Copy(connection, connection)
	return err
}

func (echoStream) Close() error {
	return nil
}

type modelsResourceOpener struct {
	models map[string]any
}

func (modelsResourceOpener) Kind() protocol.ResourceKind {
	return protocol.ResourceModels
}

func (modelsResourceOpener) Open(
	context.Context,
	resourcelease.StreamRequest,
) (server.ResourceStream, error) {
	return echoStream{}, nil
}

func (h modelsResourceOpener) ListModels(
	context.Context,
	resourcelease.StreamRequest,
) (map[string]any, error) {
	return h.models, nil
}

func (modelsResourceOpener) FlushModelsCache(resourcelease.Resolved) {}
