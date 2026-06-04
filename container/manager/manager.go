// Package manager provides a single, fx-registered service that owns the
// shared Docker client used by every container Toby starts (sandbox phases and
// MCP sidecars), tracks those containers so they can be torn down
// deterministically on session exit, and exposes sanitized introspection data.
//
// It deliberately replaces testcontainers-go's Ryuk reaper: because the service
// tracks every container it creates and terminates them from an fx OnStop hook,
// Toby owns teardown itself (Ryuk is disabled), which keeps host-network and
// Podman setups working without an extra reaper container.
package manager

import (
	"context"
	"net/url"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"

	"go.uber.org/fx"
)

// Kind classifies a tracked container for introspection.
type Kind string

const (
	KindSandbox    Kind = "sandbox"
	KindMCPSidecar Kind = "mcp-sidecar"
)

// DaemonClass describes how the Docker daemon is reached, which drives the
// sandbox networking policy (host networking vs. host-access tunnel) and the
// control-host rewrite.
type DaemonClass int

const (
	// DaemonLocalUnix is a daemon reached over a local unix socket on Linux:
	// containers can share the host network namespace.
	DaemonLocalUnix DaemonClass = iota
	// DaemonDesktop is Docker Desktop (macOS/Windows): bridge networking with a
	// magic host.docker.internal hostname.
	DaemonDesktop
	// DaemonRemote is any remote daemon (tcp/ssh) or Podman over the network:
	// the host is only reachable via testcontainers' host-access tunnel.
	DaemonRemote
)

// Meta is the introspection metadata recorded for a tracked container.
type Meta struct {
	Label   string
	Kind    Kind
	Phase   string
	Image   string
	Network string
}

// ContainerInfo is a sanitized snapshot of a tracked container. It never
// contains environment variables, argv, or secrets.
type ContainerInfo struct {
	ID      string
	Label   string
	Kind    Kind
	Phase   string
	Image   string
	Network string
}

type record struct {
	ctr       testcontainers.Container
	meta      Meta
	createdAt time.Time
}

// Service owns the shared Docker client and the registry of running containers.
type Service struct {
	mu      sync.Mutex
	records map[string]*record

	clientOnce sync.Once
	client     *testcontainers.DockerClient
	clientErr  error
	class      DaemonClass
}

// Module registers the container Service in the fx graph.
func Module() fx.Option {
	return fx.Module("container", fx.Provide(NewService))
}

// New constructs a Service without registering any lifecycle hook. It does not
// touch Docker; the client is created lazily on first use so it is safe to
// construct without a running daemon (used by tests).
func New() *Service {
	// Toby owns container teardown via terminateAll; disable testcontainers'
	// Ryuk reaper so no extra container is started (and host-network/Podman
	// setups are not disrupted).
	if _, ok := os.LookupEnv("TESTCONTAINERS_RYUK_DISABLED"); !ok {
		_ = os.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")
	}

	return &Service{records: map[string]*record{}}
}

// NewService constructs the Service and registers an fx OnStop hook that
// terminates every still-tracked container.
func NewService(lc fx.Lifecycle) *Service {
	s := New()
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			s.terminateAll(ctx)
			return nil
		},
	})
	return s
}

// Client lazily creates and caches the shared Docker client and classifies the
// daemon. Safe for concurrent use.
func (s *Service) Client(ctx context.Context) (*testcontainers.DockerClient, error) {
	s.clientOnce.Do(func() {
		cli, err := testcontainers.NewDockerClientWithOpts(ctx)
		if err != nil {
			s.clientErr = err
			return
		}

		s.client = cli
		s.class = classifyDaemon(cli.DaemonHost())
	})

	return s.client, s.clientErr
}

// DaemonClass returns how the daemon is reached, creating the client if needed.
func (s *Service) DaemonClass(ctx context.Context) (DaemonClass, error) {
	if _, err := s.Client(ctx); err != nil {
		return 0, err
	}

	return s.class, nil
}

// Ping verifies the daemon is reachable. Used by the sandbox Environment's
// Available check to surface a clear error when no daemon is running.
func (s *Service) Ping(ctx context.Context) error {
	cli, err := s.Client(ctx)
	if err != nil {
		return err
	}

	_, err = cli.Ping(ctx, client.PingOptions{})
	return err
}

// Start creates a container via testcontainers-go and tracks it. On failure it
// terminates any partially-created container and returns the error.
func (s *Service) Start(ctx context.Context, req testcontainers.GenericContainerRequest, meta Meta) (testcontainers.Container, error) {
	ctr, err := testcontainers.GenericContainer(ctx, req)
	if err != nil {
		if ctr != nil {
			_ = testcontainers.TerminateContainer(ctr)
		}
		return nil, err
	}

	if id := ctr.GetContainerID(); id != "" {
		s.mu.Lock()
		s.records[id] = &record{ctr: ctr, meta: meta, createdAt: time.Now()}
		s.mu.Unlock()
	}

	return ctr, nil
}

// Terminate stops, removes, and forgets a tracked container.
func (s *Service) Terminate(ctx context.Context, ctr testcontainers.Container) error {
	if ctr == nil {
		return nil
	}

	if id := ctr.GetContainerID(); id != "" {
		s.mu.Lock()
		delete(s.records, id)
		s.mu.Unlock()
	}

	return testcontainers.TerminateContainer(ctr, testcontainers.StopTimeout(10*time.Second))
}

// Forget drops a container from the registry without stopping it (used in debug
// mode, where containers are intentionally left running for inspection).
func (s *Service) Forget(ctr testcontainers.Container) {
	if ctr == nil {
		return
	}

	if id := ctr.GetContainerID(); id != "" {
		s.mu.Lock()
		delete(s.records, id)
		s.mu.Unlock()
	}
}

func (s *Service) terminateAll(_ context.Context) {
	s.mu.Lock()
	records := make([]*record, 0, len(s.records))
	for _, rec := range s.records {
		records = append(records, rec)
	}
	s.records = map[string]*record{}
	s.mu.Unlock()

	for _, rec := range records {
		_ = testcontainers.TerminateContainer(rec.ctr, testcontainers.StopTimeout(5*time.Second))
	}
}

// Snapshot returns sanitized metadata for every tracked container, sorted by
// label then phase, for introspection resources.
func (s *Service) Snapshot() []ContainerInfo {
	s.mu.Lock()
	infos := make([]ContainerInfo, 0, len(s.records))
	for id, rec := range s.records {
		infos = append(infos, ContainerInfo{
			ID:      shortID(id),
			Label:   rec.meta.Label,
			Kind:    rec.meta.Kind,
			Phase:   rec.meta.Phase,
			Image:   rec.meta.Image,
			Network: rec.meta.Network,
		})
	}
	s.mu.Unlock()

	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Label != infos[j].Label {
			return infos[i].Label < infos[j].Label
		}
		if infos[i].Phase != infos[j].Phase {
			return infos[i].Phase < infos[j].Phase
		}
		return infos[i].ID < infos[j].ID
	})
	return infos
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func classifyDaemon(host string) DaemonClass {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return DaemonDesktop
	}

	scheme := ""
	if u, err := url.Parse(host); err == nil {
		scheme = u.Scheme
	}
	switch scheme {
	case "unix", "npipe", "":
		return DaemonLocalUnix
	default:
		return DaemonRemote
	}
}
