package sidecar

// Owns one running sidecar generation and joins process reaping, stderr
// draining, overlay cleanup, mount release, and OCI lease release.

import (
	"context"
	"fmt"
	"sync"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
)

// Process is one fully started agent-owned sidecar.
type Process struct {
	background  bwrap.BackgroundProcess
	directories *bwrap.RunDirectories
	prepared    *Prepared
	runtimePath string
	drainDone   <-chan error
	logger      *diagnostic.Logger

	done chan struct{}

	mu  sync.Mutex
	err error
}

var _ resource.Instance = (*Process)(nil)

func newProcess(
	background bwrap.BackgroundProcess,
	directories *bwrap.RunDirectories,
	prepared *Prepared,
	runtimePath string,
	drainDone <-chan error,
	logger *diagnostic.Logger,
) *Process {
	process := &Process{
		background:  background,
		directories: directories,
		prepared:    prepared,
		runtimePath: runtimePath,
		drainDone:   drainDone,
		logger:      logger,
		done:        make(chan struct{}),
	}
	go process.wait()

	return process
}

// RuntimePath returns the diagnostic host path of this generation's private
// runtime directory while it is live.
func (p *Process) RuntimePath() string {
	if p == nil {
		return ""
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
		return ""
	default:
		return p.runtimePath
	}
}

// Done closes after the process is reaped and all sidecar capabilities are
// released.
func (p *Process) Done() <-chan struct{} {
	if p == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return p.done
}

// Err returns the background process result.
func (p *Process) Err() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Stop requests graceful payload termination.
func (p *Process) Stop(ctx context.Context) error {
	if p == nil || p.background == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("stop sidecar context is nil")
	}

	return p.background.Stop(ctx)
}

// Kill forces the complete Bubblewrap tree to terminate.
func (p *Process) Kill(ctx context.Context) error {
	if p == nil || p.background == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("kill sidecar context is nil")
	}

	return p.background.Kill(ctx)
}

func (p *Process) wait() {
	<-p.background.Done()

	result := p.background.Err()
	if p.drainDone != nil {
		p.logger.DebugError("drain sidecar stderr", <-p.drainDone)
	}
	p.logger.DebugError(
		"close sidecar run directories",
		p.directories.Close(),
	)
	p.logger.DebugError(
		"close prepared sidecar",
		p.prepared.Close(),
	)

	p.mu.Lock()
	p.err = result
	p.runtimePath = ""
	p.mu.Unlock()
	close(p.done)
}
