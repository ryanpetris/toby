package resource

// A backend is one shared MCP sidecar, refcounted across projects. Its container (and,
// for stdio, its bridge) starts on the first lease and stops when the last lease is
// released. Local stdio backends expose a streamable-HTTP handler; local http and
// remote backends expose an upstream URL. Every state transition is guarded by mu;
// ready lets concurrent acquirers join the first one's bring-up instead of racing it.

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type backend struct {
	key    string
	name   string
	remote bool
	spec   SidecarSpec // local only

	runner *DockerRunner

	mu        sync.Mutex
	refs      int
	status    Status
	lastError string
	exitCode  int
	updatedAt time.Time

	// Local stdio: the bridge fronting the sidecar. Local http / remote: the upstream.
	bridge          *StdioBridge
	upstreamURL     string
	upstreamHeaders http.Header

	handle *ProcessHandle
	cancel context.CancelFunc

	ready    chan struct{} // closed when bring-up settles (for concurrent acquirers)
	startErr error
}

func newBackend(key, name string, remote bool, spec SidecarSpec, runner *DockerRunner) *backend {
	b := &backend{
		key:       key,
		name:      name,
		remote:    remote,
		spec:      spec,
		runner:    runner,
		status:    StatusRegistered,
		updatedAt: time.Now(),
		ready:     make(chan struct{}),
	}
	// The stdio bridge is created once and reused across restarts, so the streamable
	// HTTP handler projects register stays valid — a restart just re-attaches it to the
	// new sidecar process.
	if !remote && spec.Transport != TransportHTTP {
		b.bridge = NewStdioBridge(name)
	}
	return b
}

// bringUp starts the backend once and signals ready. http and remote are synchronous
// and may fail the acquire; stdio starts its container asynchronously so a sidecar that
// cannot start surfaces as Failed status rather than failing the whole launch.
func (b *backend) bringUp(ctx context.Context, upstreamURL string, headers http.Header) {
	b.startErr = b.launch(ctx, upstreamURL, headers)
	close(b.ready)
}

func (b *backend) launch(ctx context.Context, upstreamURL string, headers http.Header) error {
	if b.remote {
		b.set(func() { b.upstreamURL, b.upstreamHeaders, b.status = upstreamURL, headers, StatusRunning })
		return nil
	}

	runCtx, cancel := context.WithCancel(context.Background())
	b.set(func() { b.cancel, b.status = cancel, StatusStarting })

	if b.spec.Transport == TransportHTTP {
		url, spec, err := b.runner.PrepareHTTP(ctx, b.spec)
		if err != nil {
			cancel()
			b.fail(err)
			return err
		}
		handle, err := b.runner.Start(runCtx, spec)
		if err != nil {
			cancel()
			b.fail(err)
			return err
		}
		b.set(func() { b.spec, b.upstreamURL, b.handle, b.status = spec, url, handle, StatusRunning })
		go b.monitor(runCtx, handle)
		return nil
	}

	// stdio: the bridge (created once in newBackend) is usable immediately; start the
	// container asynchronously and attach the bridge to it.
	go b.startStdio(runCtx, cancel)
	return nil
}

func (b *backend) startStdio(runCtx context.Context, cancel context.CancelFunc) {
	handle, err := b.runner.Start(runCtx, b.spec)
	if err != nil {
		cancel()
		b.fail(err)
		return
	}
	b.set(func() { b.handle, b.status = handle, StatusRunning })
	go func() {
		if err := b.bridge.Attach(runCtx, handle); err != nil {
			b.fail(err)
		}
	}()
	b.monitor(runCtx, handle)
}

// restart stops the sidecar and brings it up again, re-attaching the stable stdio
// bridge to the new process. For http the mapped port (and thus the upstream URL) may
// change; existing project registrations continue to point at the old URL.
func (b *backend) restart(ctx context.Context) error {
	_ = b.stop(ctx)
	b.mu.Lock()
	b.ready = make(chan struct{})
	b.startErr = nil
	b.mu.Unlock()
	b.bringUp(ctx, b.upstreamURL, b.upstreamHeaders)
	return b.startErr
}

// monitor records the sidecar's exit, unless it was stopped deliberately.
func (b *backend) monitor(runCtx context.Context, handle *ProcessHandle) {
	result := <-handle.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status == StatusStopped {
		return
	}
	b.exitCode = result.ExitCode
	b.updatedAt = time.Now()
	if result.Err != nil && runCtx.Err() == nil {
		b.status = StatusFailed
		b.lastError = result.Err.Error()
		return
	}
	b.status = StatusExited
}

// stop tears the sidecar container down.
func (b *backend) stop(ctx context.Context) error {
	b.mu.Lock()
	cancel := b.cancel
	handle := b.handle
	b.status = StatusStopped
	b.updatedAt = time.Now()
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if handle != nil {
		return handle.Stop(ctx)
	}
	return nil
}

func (b *backend) fail(err error) {
	if err == nil {
		return
	}
	b.set(func() {
		b.status = StatusFailed
		b.lastError = err.Error()
	})
}

// set applies a mutation under the lock and stamps updatedAt.
func (b *backend) set(mutate func()) {
	b.mu.Lock()
	mutate()
	b.updatedAt = time.Now()
	b.mu.Unlock()
}

// handler returns the streamable-HTTP handler for a stdio backend (nil otherwise).
func (b *backend) handler() http.Handler {
	b.mu.Lock()
	bridge := b.bridge
	b.mu.Unlock()
	if bridge == nil {
		return nil
	}
	return bridge.Handler()
}

func (b *backend) upstream() (string, http.Header) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.upstreamURL, b.upstreamHeaders
}

func (b *backend) snapshot() StatusSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	pid := 0
	if b.handle != nil {
		pid = b.handle.PID()
	}
	var info map[string]any
	if !b.remote {
		info = cloneRuntimeInfo(b.runner.RuntimeInfo(b.spec, b.spec.Debug))
	}
	return StatusSnapshot{
		Name:        b.name,
		Status:      b.status,
		Transport:   b.spec.Transport,
		PID:         pid,
		ExitCode:    b.exitCode,
		LastError:   b.lastError,
		UpdatedAt:   b.updatedAt,
		RuntimeInfo: info,
	}
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
