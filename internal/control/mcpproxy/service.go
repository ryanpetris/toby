// Package mcpproxy publishes a project's configured MCP servers to its agent through
// the project's reverse proxy. It is the per-project registration layer: for each
// enabled server it acquires a lease on the shared, daemon-root backend
// (internal/daemon/resource) and registers that backend on this project's proxy under
// a fresh URL — so identical servers across projects share one container while each
// project gets its own proxy URL. The containers themselves live in the shared
// registry; this layer owns only the per-project registrations.
package mcpproxy

import (
	"context"
	"fmt"
	"sort"
	"sync"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/tunnel"
	"petris.dev/toby/internal/daemon/resource"

	"go.uber.org/fx"
)

// Defaults are the sidecar defaults threaded to the shared registry.
type Defaults = resource.Defaults

// StatusSnapshot is the per-server status reported to the session MCP tools.
type StatusSnapshot = resource.StatusSnapshot

type ServiceParams struct {
	fx.In

	Proxy    *httpproxy.Service `optional:"true"`
	Registry *resource.Registry
}

// Service registers this project's MCP backends on its proxy.
type Service struct {
	proxy    *httpproxy.Service
	registry *resource.Registry

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	name  string
	url   string
	lease *resource.Lease
}

func NewService(params ServiceParams) (*Service, error) {
	return &Service{proxy: params.Proxy, registry: params.Registry, entries: map[string]*entry{}}, nil
}

// Configure acquires a shared backend for every enabled server and registers it on this
// project's proxy. Re-configuring releases the previous leases first.
func (s *Service) Configure(ctx context.Context, cfg *appconfig.Service, defaults Defaults) error {
	if s == nil {
		return fmt.Errorf("mcp proxy service is not configured")
	}
	if s.proxy == nil {
		return fmt.Errorf("http proxy service is not configured")
	}
	if s.registry == nil {
		return fmt.Errorf("mcp backend registry is not configured")
	}

	servers := map[string]appconfig.MCPServer{}
	if cfg != nil {
		servers = cfg.MCPServers()
	}
	names := make([]string, 0, len(servers))
	for name, server := range servers {
		if server.Enabled() {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	s.releaseAll()
	entries := make(map[string]*entry, len(names))
	for _, name := range names {
		e, err := s.configureEntry(ctx, name, servers[name], defaults)
		if err != nil {
			return err
		}
		entries[name] = e
	}
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	return nil
}

func (s *Service) configureEntry(ctx context.Context, name string, server appconfig.MCPServer, defaults Defaults) (*entry, error) {
	lease, err := s.registry.Acquire(ctx, name, server, defaults)
	if err != nil {
		return nil, err
	}
	id, err := s.register(lease)
	if err != nil {
		lease.Release()
		return nil, fmt.Errorf("mcp.%s: %w", name, err)
	}
	return &entry{name: name, url: tunnel.ProxyBaseURL(id), lease: lease}, nil
}

// register installs the lease's backend on this project's proxy: the streamable-HTTP
// handler for stdio, or the upstream URL + headers for http/remote.
func (s *Service) register(lease *resource.Lease) (string, error) {
	if handler := lease.Handler(); handler != nil {
		return s.proxy.RegisterHandler(handler)
	}
	url, headers := lease.Upstream()
	if url == "" {
		return "", fmt.Errorf("backend %q has no upstream", lease.Name())
	}
	return s.proxy.RegisterUpstream(url, headers)
}

// URL returns the proxied URL for a configured server.
func (s *Service) URL(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	e, ok := s.entries[name]
	s.mu.Unlock()
	if !ok || e == nil || e.url == "" {
		return "", false
	}
	return e.url, true
}

// Status reports each configured server's backend status.
func (s *Service) Status() []StatusSnapshot {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]StatusSnapshot, 0, len(names))
	for _, name := range names {
		result = append(result, s.entries[name].lease.Snapshot())
	}
	s.mu.Unlock()
	return result
}

// Start, Stop, and Restart act on the shared backend for a server, affecting every
// project using it.
func (s *Service) Start(ctx context.Context, name string) error {
	return s.act(ctx, name, s.registry.Start)
}
func (s *Service) Stop(ctx context.Context, name string) error {
	return s.act(ctx, name, s.registry.Stop)
}
func (s *Service) Restart(ctx context.Context, name string) error {
	return s.act(ctx, name, s.registry.Restart)
}

func (s *Service) act(ctx context.Context, name string, fn func(context.Context, string) error) error {
	s.mu.Lock()
	e, ok := s.entries[name]
	s.mu.Unlock()
	if !ok || e == nil {
		return fmt.Errorf("mcp %q is not configured", name)
	}
	return fn(ctx, e.lease.Key())
}

// Close releases every lease this project holds; the shared backends stop when their
// last project releases them.
func (s *Service) Close() { s.releaseAll() }

func (s *Service) releaseAll() {
	s.mu.Lock()
	entries := s.entries
	s.entries = map[string]*entry{}
	s.mu.Unlock()
	for _, e := range entries {
		if e.lease != nil {
			e.lease.Release()
		}
	}
}
