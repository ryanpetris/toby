// Package engine provides a single, fx-registered service that owns the
// shared Docker client used by every container Toby starts (sandbox phases and
// MCP sidecars), tracks those containers so they can be torn down
// deterministically on session exit, and exposes sanitized introspection data.
//
// It deliberately replaces testcontainers-go's Ryuk reaper: because the service
// tracks every container it creates and terminates them from an fx OnStop hook,
// Toby owns teardown itself (Ryuk is disabled), which keeps host-network and
// Podman setups working without an extra reaper container.
package engine

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

// Service owns the shared Docker client and the registry of running containers.
type Service struct {
	mu          sync.Mutex
	records     map[string]*record
	keepStopped bool

	clientOnce sync.Once
	client     *testcontainers.DockerClient
	clientErr  error
}

// Client lazily creates and caches the shared Docker client. Safe for concurrent
// use.
func (s *Service) Client(ctx context.Context) (*testcontainers.DockerClient, error) {
	s.clientOnce.Do(func() {
		cli, err := testcontainers.NewDockerClientWithOpts(ctx)
		if err != nil {
			s.clientErr = err
			return
		}

		s.client = cli
	})

	return s.client, s.clientErr
}

// Ping verifies the daemon is reachable. The session runs it as a preflight
// check to surface a clear error when no daemon is running (or DOCKER_HOST is
// unreachable) before building a sandbox.
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

// SetKeepStopped sets the teardown policy: when keep is true, a torn-down
// container is stopped but left on the host (only dropped from the registry) so
// a debug session can inspect it after exit; when false it is stopped and
// removed. The launch sets this from its debug flag before any teardown runs.
func (s *Service) SetKeepStopped(keep bool) {
	s.mu.Lock()
	s.keepStopped = keep
	s.mu.Unlock()
}

// Terminate tears a tracked container down and drops it from the registry. The
// container is always stopped; it is removed unless the keep-stopped policy is
// set (debug), in which case it is left on the host for inspection.
func (s *Service) Terminate(ctx context.Context, ctr testcontainers.Container) error {
	if ctr == nil {
		return nil
	}

	s.mu.Lock()
	keep := s.keepStopped
	if id := ctr.GetContainerID(); id != "" {
		delete(s.records, id)
	}
	s.mu.Unlock()

	if keep {
		return s.stopContainer(ctx, ctr, 10*time.Second)
	}
	return testcontainers.TerminateContainer(ctr, testcontainers.StopTimeout(10*time.Second))
}

// stopContainer stops a container without removing it, so a debug session can
// inspect it after exit.
func (s *Service) stopContainer(ctx context.Context, ctr testcontainers.Container, timeout time.Duration) error {
	id := ctr.GetContainerID()
	if id == "" {
		return nil
	}

	cli, err := s.Client(ctx)
	if err != nil {
		return err
	}

	secs := int(timeout.Seconds())
	_, err = cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &secs})
	return err
}

func (s *Service) terminateAll(ctx context.Context) {
	s.mu.Lock()
	keep := s.keepStopped
	records := make([]*record, 0, len(s.records))
	for _, rec := range s.records {
		records = append(records, rec)
	}
	s.records = map[string]*record{}
	s.mu.Unlock()

	for _, rec := range records {
		if keep {
			_ = s.stopContainer(ctx, rec.ctr, 5*time.Second)
			continue
		}
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
