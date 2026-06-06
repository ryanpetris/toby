// Package mcpproxy exposes a session's configured MCP servers to the agent
// through a single proxy: remote servers are registered as upstreams, local
// servers run as Docker sidecars (stdio bridged to streamable HTTP, or HTTP
// reached through a mapped port), each surfaced at a control-host proxy URL.
//
// This file holds the Service that owns the per-server entries: Configure
// builds them from config, URL looks one up, StartAll/StopAll/Status drive and
// report the local sidecars.
package mcpproxy

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/tunnel"

	"go.uber.org/fx"
)

type ServiceParams struct {
	fx.In

	Proxy  *httpproxy.Service `optional:"true"`
	Runner *DockerRunner
}

type Service struct {
	proxy  *httpproxy.Service
	runner *DockerRunner

	mu      sync.RWMutex
	entries map[string]*Entry
}

type Entry struct {
	Name      string
	URL       string
	Server    appconfig.MCPServer
	Spec      SidecarSpec
	Bridge    *StdioBridge
	Remote    bool
	Status    Status
	LastError string
	ExitCode  int
	UpdatedAt time.Time

	cancel context.CancelFunc
	handle *ProcessHandle
}

func NewService(params ServiceParams) (*Service, error) {
	return &Service{proxy: params.Proxy, runner: params.Runner, entries: map[string]*Entry{}}, nil
}

func (s *Service) Configure(ctx context.Context, cfg *appconfig.Service, defaults Defaults) error {
	if s == nil {
		return fmt.Errorf("mcp proxy service is not configured")
	}
	if s.proxy == nil {
		return fmt.Errorf("http proxy service is not configured")
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

	// Resolve (and build, if mcp.build is set) the default sidecar image once,
	// but only when an enabled local server will actually fall back to it.
	if needsDefaultImage(servers, names) {
		image, err := resolveDefaultImage(ctx, defaults)
		if err != nil {
			return err
		}
		defaults.Image = image
	} else {
		defaults.Image = ""
	}

	entries := make(map[string]*Entry, len(names))
	for _, name := range names {
		entry, err := s.configureEntry(ctx, name, servers[name], defaults)
		if err != nil {
			return err
		}
		entries[name] = entry
	}
	s.mu.Lock()
	s.entries = entries
	s.mu.Unlock()
	return nil
}

func (s *Service) configureEntry(ctx context.Context, name string, server appconfig.MCPServer, defaults Defaults) (*Entry, error) {
	if server.Remote() {
		headers, err := server.Headers()
		if err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		id, err := s.proxy.RegisterUpstream(server.URL(), headers)
		if err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		return &Entry{Name: name, URL: tunnel.ProxyBaseURL(id), Server: server, Remote: true, Status: StatusRunning, UpdatedAt: time.Now()}, nil
	}
	if !server.Local() {
		return nil, fmt.Errorf("mcp.%s command or url is required", name)
	}
	spec, err := sidecarSpec(name, server, defaults)
	if err != nil {
		return nil, err
	}
	entry := &Entry{Name: name, Server: server, Spec: spec, Status: StatusRegistered, UpdatedAt: time.Now()}
	switch spec.Transport {
	case TransportStdio:
		bridge := NewStdioBridge(name)
		id, err := s.proxy.RegisterHandler(bridge.Handler())
		if err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		entry.Bridge = bridge
		entry.URL = tunnel.ProxyBaseURL(id)
	case TransportHTTP:
		baseURL, spec, err := s.runner.PrepareHTTP(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		id, err := s.proxy.RegisterUpstream(baseURL, nil)
		if err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		entry.Spec = spec
		entry.URL = tunnel.ProxyBaseURL(id)
	default:
		return nil, fmt.Errorf("mcp.%s.transport is unsupported", name)
	}
	return entry, nil
}

func (s *Service) URL(name string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.RLock()
	entry, ok := s.entries[name]
	s.mu.RUnlock()
	if !ok || entry == nil || entry.URL == "" {
		return "", false
	}
	return entry.URL, true
}

func (s *Service) StartAll(ctx context.Context) {
	for _, entry := range s.localEntries() {
		go s.start(ctx, entry)
	}
}

func (s *Service) StopAll(ctx context.Context) {
	for _, entry := range s.localEntries() {
		_ = s.stop(ctx, entry)
	}
}

func (s *Service) Status() []StatusSnapshot {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	names := make([]string, 0, len(s.entries))
	for name := range s.entries {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]StatusSnapshot, 0, len(names))
	for _, name := range names {
		result = append(result, s.statusSnapshot(s.entries[name]))
	}
	s.mu.RUnlock()
	return result
}

func (s *Service) localEntries() []*Entry {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	entries := make([]*Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry != nil && !entry.Remote {
			entries = append(entries, entry)
		}
	}
	s.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func (s *Service) statusSnapshot(entry *Entry) StatusSnapshot {
	if entry == nil {
		return StatusSnapshot{}
	}
	pid := 0
	if entry.handle != nil {
		pid = entry.handle.PID()
	}
	debug := entry.Spec.Debug
	return StatusSnapshot{Name: entry.Name, Status: entry.Status, URL: entry.URL, Transport: entry.Spec.Transport, PID: pid, ExitCode: entry.ExitCode, LastError: entry.LastError, UpdatedAt: entry.UpdatedAt, RuntimeInfo: cloneRuntimeInfo(s.runner.RuntimeInfo(entry.Spec, debug))}
}

func cloneRuntimeInfo(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
