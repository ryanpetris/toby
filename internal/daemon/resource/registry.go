// Package resource is the daemon-root registry of shared MCP sidecar backends. A
// backend is keyed by its server's resolved config, so identical configuration across
// projects shares one container (and, for stdio, one bridge), refcounted; a changed
// config yields a new backend while in-use ones linger until released. Providers and
// remote MCP servers are "backends" too, but with no container — just an upstream
// descriptor. The per-project mcpproxy layer acquires a lease and registers it on its
// own proxy.
package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"

	appconfig "petris.dev/toby/internal/config/app"
)

// Registry owns the shared, refcounted backends.
type Registry struct {
	runner *DockerRunner

	mu       sync.Mutex
	backends map[string]*backend
}

// NewRegistry builds a registry over a sidecar runner.
func NewRegistry(runner *DockerRunner) *Registry {
	return &Registry{runner: runner, backends: map[string]*backend{}}
}

// Acquire returns a lease on the backend for server, starting it on the first lease.
func (r *Registry) Acquire(ctx context.Context, name string, server appconfig.MCPServer, defaults Defaults) (*Lease, error) {
	if server.Remote() {
		headers, err := server.Headers()
		if err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		return r.acquire(ctx, remoteKey(name, server.URL(), headers), name, true, SidecarSpec{}, server.URL(), headers)
	}
	if !server.Local() {
		return nil, fmt.Errorf("mcp.%s command or url is required", name)
	}

	image, err := r.resolveImage(ctx, server, defaults)
	if err != nil {
		return nil, err
	}
	defaults.Image = image
	spec, err := sidecarSpec(name, server, defaults)
	if err != nil {
		return nil, err
	}
	return r.acquire(ctx, localKey(spec), name, false, spec, "", nil)
}

func (r *Registry) acquire(ctx context.Context, key, name string, remote bool, spec SidecarSpec, upstreamURL string, headers http.Header) (*Lease, error) {
	b, created := r.getOrCreate(key, name, remote, spec)
	if created {
		b.bringUp(ctx, upstreamURL, headers)
	} else {
		<-b.ready
	}
	if err := b.startErr; err != nil {
		r.release(b)
		return nil, err
	}
	return &Lease{registry: r, backend: b}, nil
}

func (r *Registry) getOrCreate(key, name string, remote bool, spec SidecarSpec) (*backend, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.backends[key]
	created := false
	if !ok {
		b = newBackend(key, name, remote, spec, r.runner)
		r.backends[key] = b
		created = true
	}
	b.refs++
	return b, created
}

// release drops a lease's reference; the last release stops and removes the backend.
func (r *Registry) release(b *backend) {
	r.mu.Lock()
	b.refs--
	remove := b.refs <= 0
	if remove {
		delete(r.backends, b.key)
	}
	r.mu.Unlock()
	if remove {
		_ = b.stop(context.Background())
	}
}

// Restart restarts the shared backend for key (affecting every project using it).
func (r *Registry) Restart(ctx context.Context, key string) error {
	b, err := r.local(key)
	if err != nil {
		return err
	}
	return b.restart(ctx)
}

// Start (re)starts a shared backend's container without changing its refcount.
func (r *Registry) Start(ctx context.Context, key string) error {
	b, err := r.local(key)
	if err != nil {
		return err
	}
	return b.restart(ctx)
}

// Stop stops a shared backend's container without dropping its refcount, so it can be
// restarted. This affects every project using it.
func (r *Registry) Stop(ctx context.Context, key string) error {
	b, err := r.local(key)
	if err != nil {
		return err
	}
	return b.stop(ctx)
}

// local looks up a non-remote backend by key.
func (r *Registry) local(key string) (*backend, error) {
	r.mu.Lock()
	b := r.backends[key]
	r.mu.Unlock()
	if b == nil {
		return nil, fmt.Errorf("mcp backend is not running")
	}
	if b.remote {
		return nil, fmt.Errorf("mcp %q is remote", b.name)
	}
	return b, nil
}

// resolveImage resolves the default sidecar image for a local server lacking its own.
func (r *Registry) resolveImage(ctx context.Context, server appconfig.MCPServer, defaults Defaults) (string, error) {
	if server.Image() != "" {
		return "", nil // the server's own image wins; default not needed
	}
	return resolveDefaultImage(ctx, defaults)
}

// localKey fingerprints a local sidecar's spec so identical configs share a backend and
// a changed config gets a fresh one.
func localKey(spec SidecarSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "local\x00%s\x00%s\x00%s\x00%d\x00%s\x00", spec.Name, spec.Transport, spec.Image, spec.HTTPPort, spec.HTTPPath)
	for _, part := range spec.Command {
		fmt.Fprintf(h, "%s\x00", part)
	}
	writeSortedEnv(h, spec.Env)
	return hex.EncodeToString(h.Sum(nil))
}

// remoteKey fingerprints a remote/provider upstream by url and headers.
func remoteKey(name, url string, headers http.Header) string {
	h := sha256.New()
	fmt.Fprintf(h, "remote\x00%s\x00%s\x00", name, url)
	writeSortedHeaders(h, headers)
	return hex.EncodeToString(h.Sum(nil))
}

func writeSortedHeaders(h io.Writer, headers http.Header) {
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%v\x00", k, headers[k])
	}
}

func writeSortedEnv(h io.Writer, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\x00", k, m[k])
	}
}
