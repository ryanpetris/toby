// Package home is the daemon-root registry of shared per-profile home containers.
// One home container per profile owns the shared /toby/home volume and runs the
// `sandbox home` manager (files + streamed exec). It is shared across every project
// on that profile and held for the daemon's lifetime (torn down at shutdown), so
// installed tools and tool state persist and are shared. A per-home install mutex
// serializes installs into the shared home.
package home

import (
	"context"
	"fmt"
	"os"
	"sync"

	"google.golang.org/grpc"

	"petris.dev/toby/container/engine"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/control/host"
	"petris.dev/toby/internal/control/stdio"
	"petris.dev/toby/internal/control/tunnel"
	sandbox "petris.dev/toby/sandbox/runtime"

	"github.com/moby/moby/client"
)

// Registry stands up and tracks the shared home containers, keyed by profile.
type Registry struct {
	engine *engine.Service

	mu      sync.Mutex
	entries map[string]*entry
}

// entry is one live home container plus its control link.
type entry struct {
	profile   string
	manager   *sandbox.Manager
	server    *tunnel.Server
	grpcSrv   *grpc.Server
	client    *host.SandboxClient
	baseEnv   []string
	uid, gid  int
	installMu sync.Mutex
	refs      int
}

// NewRegistry constructs the home registry over the daemon-root engine.
func NewRegistry(eng *engine.Service) *Registry {
	return &Registry{engine: eng, entries: map[string]*entry{}}
}

// Lease is a hold on a profile's home container; the caller uses it for files/exec
// and releases it when its session ends.
type Lease struct {
	reg  *Registry
	e    *entry
	once sync.Once
}

// Client returns the home manager control client (files + streamed exec).
func (l *Lease) Client() *host.SandboxClient { return l.e.client }

// BaseEnv returns the home container's base environment for seeding installs.
func (l *Lease) BaseEnv() []string { return l.e.baseEnv }

// UID/GID are the invoking user the tool and its installs run as.
func (l *Lease) UID() int { return l.e.uid }
func (l *Lease) GID() int { return l.e.gid }

// InstallLock serializes installs into the shared home.
func (l *Lease) InstallLock() *sync.Mutex { return &l.e.installMu }

// Release drops this session's hold (the home container persists until shutdown).
func (l *Lease) Release() {
	l.once.Do(func() {
		l.reg.mu.Lock()
		if l.e.refs > 0 {
			l.e.refs--
		}
		l.reg.mu.Unlock()
	})
}

// Acquire returns a lease on the profile's home container, standing it up on first
// use with the given image and read-only toby-binary volume.
func (r *Registry) Acquire(ctx context.Context, profile, image, binVol string) (*Lease, error) {
	r.mu.Lock()
	if e, ok := r.entries[profile]; ok {
		e.refs++
		r.mu.Unlock()
		return &Lease{reg: r, e: e}, nil
	}
	r.mu.Unlock()

	e, err := r.standUp(ctx, profile, image, binVol)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if existing, ok := r.entries[profile]; ok {
		// Lost a race; adopt the existing entry and tear down ours.
		existing.refs++
		r.mu.Unlock()
		e.close(context.Background())
		return &Lease{reg: r, e: existing}, nil
	}
	e.refs = 1
	r.entries[profile] = e
	r.mu.Unlock()
	return &Lease{reg: r, e: e}, nil
}

// standUp creates the home container, serves its Control link, seeds the base env,
// and chowns the shared home to the invoking user once.
func (r *Registry) standUp(ctx context.Context, profile, image, binVol string) (*entry, error) {
	manager, err := sandbox.StandUpManager(ctx, r.engine, sandbox.ManagerSpec{
		Name:       "toby.home." + profile,
		Label:      "home." + profile,
		Kind:       "home",
		Image:      image,
		BinVolume:  binVol,
		HomeVolume: mount.HomeVolume(profile),
	})
	if err != nil {
		return nil, err
	}

	ready := make(chan struct{}, 1)
	server := tunnel.NewServer(nil, func(string) {
		select {
		case ready <- struct{}{}:
		default:
		}
	})
	sc := host.NewSandboxClient(server)
	server.SetControlHandler(sc.HandleControl)
	grpcSrv := grpc.NewServer()
	tunnel.RegisterTunnelServer(grpcSrv, server)
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = grpcSrv.Serve(stdio.NewListener(manager.Conn()))
	}()

	e := &entry{
		profile: profile,
		manager: manager,
		server:  server,
		grpcSrv: grpcSrv,
		client:  sc,
		uid:     os.Getuid(),
		gid:     os.Getgid(),
	}

	select {
	case <-ready:
	case <-serveDone:
		e.close(context.Background())
		return nil, fmt.Errorf("home manager exited before reporting ready")
	case <-ctx.Done():
		e.close(context.Background())
		return nil, ctx.Err()
	}

	baseEnv, err := r.baseEnv(ctx, manager.ContainerID())
	if err != nil {
		e.close(context.Background())
		return nil, err
	}
	e.baseEnv = baseEnv

	if err := e.chownHome(ctx); err != nil {
		e.close(context.Background())
		return nil, err
	}
	return e, nil
}

func (r *Registry) baseEnv(ctx context.Context, id string) ([]string, error) {
	cli, err := r.engine.Client(ctx)
	if err != nil {
		return nil, err
	}
	info, err := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	if info.Container.Config == nil {
		return nil, nil
	}
	return info.Container.Config.Env, nil
}

// Shutdown tears down every home container (daemon stop).
func (r *Registry) Shutdown() {
	r.mu.Lock()
	entries := make([]*entry, 0, len(r.entries))
	for _, e := range r.entries {
		entries = append(entries, e)
	}
	r.entries = map[string]*entry{}
	r.mu.Unlock()
	for _, e := range entries {
		e.close(context.Background())
	}
}

// chownHome chowns the shared home to the invoking user once, guarded by a marker so
// warm home containers skip the (potentially slow) recursive chown.
func (e *entry) chownHome(ctx context.Context) error {
	marker := "/toby/home/.toby/.owned"
	script := fmt.Sprintf("[ -f %s ] && exit 0; mkdir -p /toby/home/.toby && chown %d:%d /toby/home /toby/home/.toby && touch %s", marker, e.uid, e.gid, marker)
	if _, err := e.client.ExecStream(ctx, []string{"sh", "-c", script}, nil, "/toby/home", 0, 0, nil); err != nil {
		return err
	}
	return nil
}

func (e *entry) close(ctx context.Context) {
	if e.grpcSrv != nil {
		e.grpcSrv.Stop()
	}
	if e.server != nil {
		_ = e.server.Close()
	}
	if e.manager != nil {
		e.manager.Close(ctx)
	}
}
