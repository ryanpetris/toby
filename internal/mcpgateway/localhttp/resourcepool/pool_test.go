package resourcepool

// Exercises canonical sharing, run-local connector revocation, generation
// invalidation, and idle process shutdown.

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
	"petris.dev/toby/internal/mcpgateway/localhttp"
	"petris.dev/toby/internal/mcpgateway/sidecar"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/sandbox/pasta"
)

func TestPoolSharesProcessButNotRunConnectorLeases(t *testing.T) {
	t.Parallel()

	starter := &fakeStarter{}
	pool := newTestPool(t, starter)
	firstPrepared, err := pool.Prepare(
		t.Context(),
		testDefinition("secret"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondPrepared, err := pool.Prepare(
		t.Context(),
		testDefinition("secret"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if starter.calls() != 0 {
		t.Fatal("Prepare started a process")
	}

	first, err := firstPrepared.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondPrepared.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if starter.calls() != 1 {
		t.Fatalf("shared process starts = %d, want 1", starter.calls())
	}

	firstConnector, err := first.OpenConnector(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	secondConnector, err := second.OpenConnector(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first.Revoke()
	select {
	case <-firstConnector.Done():
	default:
		t.Fatal("first run connector remained live after revocation")
	}
	select {
	case <-secondConnector.Done():
		t.Fatal("first run revocation closed the second run connector")
	default:
	}

	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondConnector.Done():
	default:
		t.Fatal("second run connector remained live after release")
	}

	if starter.last().stopped() {
		t.Fatal("shared process stopped before its idle timeout")
	}

	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !starter.last().stopped() {
		t.Fatal("pool shutdown did not stop the shared process")
	}
}

func TestProcessExitInvalidatesGenerationConnector(t *testing.T) {
	t.Parallel()

	starter := &fakeStarter{}
	pool := newTestPool(t, starter)
	prepared, err := pool.Prepare(
		t.Context(),
		testDefinition("secret"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := prepared.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	connector, err := lease.OpenConnector(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	starter.last().exit(errors.New("unexpected exit"))

	select {
	case <-connector.Done():
	case <-time.After(time.Second):
		t.Fatal("generation connector remained live after process exit")
	}
	if !errors.Is(connector.Err(), resource.ErrResourceExited) {
		t.Fatalf(
			"generation connector error = %v, want ErrResourceExited",
			connector.Err(),
		)
	}
	replacement, err := lease.OpenConnector(t.Context())
	if err != nil {
		t.Fatalf("reopen connector after process exit: %v", err)
	}
	if starter.calls() != 2 {
		t.Fatalf(
			"process starts after reconnect = %d, want 2",
			starter.calls(),
		)
	}
	replacement.Close()

	lease.Revoke()
	if err := lease.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPoolSeparatesSensitiveResourceIdentity(t *testing.T) {
	t.Parallel()

	starter := &fakeStarter{}
	pool := newTestPool(t, starter)
	firstPrepared, err := pool.Prepare(
		t.Context(),
		testDefinition("first"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondPrepared, err := pool.Prepare(
		t.Context(),
		testDefinition("second"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := firstPrepared.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondPrepared.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if starter.calls() != 2 {
		t.Fatalf("secret-separated process starts = %d, want 2", starter.calls())
	}
	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := second.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedCloseReleasesUnacquiredMountCapabilities(t *testing.T) {
	t.Parallel()

	service, capabilities, definition := newTestMountCapabilities(t)
	pool := newTestPoolWithPlanner(
		t,
		&capabilityPlanner{
			capabilities: []*sidecar.MountCapabilities{capabilities},
		},
		&fakeStarter{},
	)

	prepared, err := pool.Prepare(
		t.Context(),
		testDefinition("secret"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if mountCapabilitiesClosed(
		t,
		service,
		capabilities,
		definition,
	) {
		t.Fatal("Prepare closed retained mount capabilities")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if !mountCapabilitiesClosed(
		t,
		service,
		capabilities,
		definition,
	) {
		t.Fatal("prepared Close retained mount capabilities")
	}

	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSharedPlanOwnsOneMountCapabilitySetUntilFinalRelease(
	t *testing.T,
) {
	t.Parallel()

	firstService, firstCapabilities, firstDefinition :=
		newTestMountCapabilities(t)
	secondService, secondCapabilities, secondDefinition :=
		newTestMountCapabilities(t)
	pool := newTestPoolWithPlanner(
		t,
		&capabilityPlanner{
			capabilities: []*sidecar.MountCapabilities{
				firstCapabilities,
				secondCapabilities,
			},
		},
		&fakeStarter{},
	)

	firstPrepared, err := pool.Prepare(
		t.Context(),
		testDefinition("secret"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondPrepared, err := pool.Prepare(
		t.Context(),
		testDefinition("secret"),
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	first, err := firstPrepared.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondPrepared.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if mountCapabilitiesClosed(
		t,
		firstService,
		firstCapabilities,
		firstDefinition,
	) {
		t.Fatal("shared plan closed its retained mount capabilities")
	}
	if !mountCapabilitiesClosed(
		t,
		secondService,
		secondCapabilities,
		secondDefinition,
	) {
		t.Fatal("duplicate plan retained a second mount capability set")
	}

	if err := first.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if mountCapabilitiesClosed(
		t,
		firstService,
		firstCapabilities,
		firstDefinition,
	) {
		t.Fatal("first release closed capabilities still used by a lease")
	}
	if err := second.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !mountCapabilitiesClosed(
		t,
		firstService,
		firstCapabilities,
		firstDefinition,
	) {
		t.Fatal("final release retained shared mount capabilities")
	}

	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type testPlanner struct{}

var _ Planner = testPlanner{}

func (testPlanner) Plan(
	_ context.Context,
	definition localhttp.Definition,
	_ mcpgateway.ProgressReporter,
) (Plan, error) {
	environment := make([]resource.EnvironmentVariable, 0, len(definition.Environment))
	for name, value := range definition.Environment {
		environment = append(environment, resource.EnvironmentVariable{
			Name:      name,
			Value:     value,
			Sensitive: true,
		})
	}

	return Plan{
		Resource: resource.Spec{
			Kind:           resource.KindMCPHTTP,
			Transport:      resource.TransportHTTP,
			ManifestDigest: "sha256:" + strings.Repeat("a", 64),
			RootFSDigest:   "sha256:" + strings.Repeat("b", 64),
			Argv:           append([]string(nil), definition.Command...),
			Workdir:        "/",
			Identity:       resource.Identity{},
			Environment:    environment,
			Endpoint: resource.Endpoint{
				Kind:   resource.EndpointUnix,
				Socket: definition.Endpoint.Socket,
				Path:   definition.Endpoint.Path,
			},
			Network:         definition.Network,
			BridgeVersion:   "1",
			ProtocolVersion: "2025-06-18",
			RequestedScope:  definition.Scope,
			RunAuthority:    resource.RunAuthorityAbsent,
		},
		Definition: definition,
	}, nil
}

type capabilityPlanner struct {
	mu           sync.Mutex
	capabilities []*sidecar.MountCapabilities
}

var _ Planner = (*capabilityPlanner)(nil)

func (p *capabilityPlanner) Plan(
	ctx context.Context,
	definition localhttp.Definition,
	progress mcpgateway.ProgressReporter,
) (Plan, error) {
	plan, err := (testPlanner{}).Plan(ctx, definition, progress)
	if err != nil {
		return Plan{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.capabilities) == 0 {
		return Plan{}, errors.New("test mount capabilities exhausted")
	}
	plan.Capabilities = p.capabilities[0]
	p.capabilities = p.capabilities[1:]

	return plan, nil
}

type fakeStarter struct {
	mu        sync.Mutex
	instances []*fakeInstance
}

var _ Starter = (*fakeStarter)(nil)

func (s *fakeStarter) Start(
	context.Context,
	Plan,
	uint64,
) (Instance, error) {
	instance := &fakeInstance{done: make(chan struct{})}
	s.mu.Lock()
	s.instances = append(s.instances, instance)
	s.mu.Unlock()
	return instance, nil
}

func (s *fakeStarter) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.instances)
}

func (s *fakeStarter) last() *fakeInstance {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.instances[len(s.instances)-1]
}

type fakeInstance struct {
	mu   sync.Mutex
	done chan struct{}
	err  error
}

var _ Instance = (*fakeInstance)(nil)

func (i *fakeInstance) Upstream() (httpbridge.Upstream, error) {
	return httpbridge.Upstream{
		Endpoint: "http://127.0.0.1:3000/mcp",
		Headers:  http.Header{"X-Local": []string{"true"}},
	}, nil
}

func (i *fakeInstance) Done() <-chan struct{} {
	return i.done
}

func (i *fakeInstance) Err() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.err
}

func (i *fakeInstance) Stop(context.Context) error {
	i.exit(nil)
	return nil
}

func (i *fakeInstance) Kill(context.Context) error {
	i.exit(nil)
	return nil
}

func (i *fakeInstance) exit(err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	select {
	case <-i.done:
		return
	default:
		i.err = err
		close(i.done)
	}
}

func (i *fakeInstance) stopped() bool {
	select {
	case <-i.done:
		return true
	default:
		return false
	}
}

func newTestPool(t *testing.T, starter Starter) *Pool {
	t.Helper()

	return newTestPoolWithPlanner(t, testPlanner{}, starter)
}

func newTestPoolWithPlanner(
	t *testing.T,
	planner Planner,
	starter Starter,
) *Pool {
	t.Helper()

	builder, err := resource.NewBuilder(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := New(
		builder,
		planner,
		starter,
		resource.Options{
			IdleTimeout:    time.Hour,
			StopGrace:      time.Second,
			KillGrace:      time.Second,
			BackoffInitial: time.Millisecond,
			BackoffMaximum: time.Millisecond,
			Jitter: func(delay time.Duration) time.Duration {
				return delay
			},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

var errTestImagePreparation = errors.New(
	"test image preparation reached",
)

type unavailableImagePreparer struct{}

var _ sidecar.ImagePreparer = unavailableImagePreparer{}

func (unavailableImagePreparer) PrepareImage(
	context.Context,
	string,
	mcpgateway.ProgressReporter,
) (sidecar.Image, error) {
	return nil, errTestImagePreparation
}

type unavailableBackgroundExecutor struct{}

var _ sidecar.BackgroundExecutor = unavailableBackgroundExecutor{}

func (unavailableBackgroundExecutor) StartBackground(
	context.Context,
	*bwrap.Invocation,
	bwrap.ProcessIO,
	bwrap.BackgroundSetup,
) (bwrap.BackgroundProcess, error) {
	return nil, errors.New("test background execution reached")
}

type unavailablePrivateNetwork struct{}

var _ sidecar.PrivateNetworkStarter = unavailablePrivateNetwork{}

func (unavailablePrivateNetwork) Start(
	context.Context,
	pasta.StartOptions,
) (pasta.Process, error) {
	return nil, errors.New("test private network startup reached")
}

func newTestMountCapabilities(
	t *testing.T,
) (
	*sidecar.Preparer,
	*sidecar.MountCapabilities,
	sidecar.Definition,
) {
	t.Helper()

	service, err := sidecar.New(
		unavailableImagePreparer{},
		new(bwrap.RunStorage),
		unavailableBackgroundExecutor{},
		unavailablePrivateNetwork{},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	definition := sidecar.Definition{
		Image:   "example.invalid/mcp:latest",
		Command: []string{"/bin/mcp"},
		Mounts: []mcpgateway.Mount{{
			Source: t.TempDir(),
			Target: "/data",
			Access: mount.AccessReadOnly,
			Scope:  resource.ScopeHome,
		}},
		Network: resource.NetworkHost,
	}
	capabilities, err := service.PinMounts(
		t.Context(),
		definition.Mounts,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := capabilities.Close(); err != nil {
			t.Error(err)
		}
	})

	return service, capabilities, definition
}

func mountCapabilitiesClosed(
	t *testing.T,
	service *sidecar.Preparer,
	capabilities *sidecar.MountCapabilities,
	definition sidecar.Definition,
) bool {
	t.Helper()

	prepared, err := service.PreparePinned(
		t.Context(),
		definition,
		capabilities,
		nil,
	)
	if prepared != nil {
		_ = prepared.Close()
		t.Fatal("test image preparer unexpectedly returned a sidecar")
	}
	switch {
	case errors.Is(err, errTestImagePreparation):
		return false
	case err != nil && strings.Contains(err.Error(), "closed"):
		return true
	default:
		t.Fatalf("inspect mount capability state: %v", err)
		return false
	}
}

func testDefinition(secret string) localhttp.Definition {
	return localhttp.Definition{
		Image:   "example.invalid/mcp@sha256:aaaa",
		Command: []string{"/bin/server"},
		Environment: map[string]string{
			"TOKEN": secret,
		},
		Endpoint: mcpgateway.Endpoint{
			Kind:   mcpgateway.EndpointUnix,
			Socket: layout.Runtime + "/mcp.sock",
			Path:   "/mcp",
		},
		Scope:   resource.ScopeUser,
		Network: resource.NetworkHost,
	}
}
