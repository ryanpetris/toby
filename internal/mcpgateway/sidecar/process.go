package sidecar

// Owns one running sidecar generation and joins process reaping, stderr
// draining, overlay cleanup, mount release, and OCI lease release.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/pasta"
)

const (
	sidecarCompanionExitReconciliationLimit = 250 * time.Millisecond
	sidecarCompanionShutdownLimit           = 2 * time.Second
)

// Process is one fully started agent-owned sidecar.
type Process struct {
	background  bwrap.BackgroundProcess
	network     pasta.Process
	directories *bwrap.RunDirectories
	prepared    *Prepared
	runtimePath string
	drainDone   <-chan error
	logger      *diagnostic.Logger

	done chan struct{}

	mu      sync.Mutex
	err     error
	forcing bool
}

var _ resource.Instance = (*Process)(nil)

func newProcess(
	background bwrap.BackgroundProcess,
	network pasta.Process,
	directories *bwrap.RunDirectories,
	prepared *Prepared,
	runtimePath string,
	drainDone <-chan error,
	logger *diagnostic.Logger,
) *Process {
	process := &Process{
		background:  background,
		network:     network,
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

	p.mu.Lock()
	p.forcing = true
	p.mu.Unlock()

	var networkErr error
	if p.network != nil {
		networkErr = p.network.Kill(ctx)
	}
	return errors.Join(p.background.Kill(ctx), networkErr)
}

func (p *Process) wait() {
	result := p.waitProcesses()
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

func (p *Process) waitProcesses() error {
	if p.network == nil {
		<-p.background.Done()
		return p.background.Err()
	}

	select {
	case <-p.background.Done():
		p.finishNetwork()
		return p.background.Err()
	case <-p.network.Done():
		select {
		case <-p.background.Done():
			return p.background.Err()
		case <-time.After(sidecarCompanionExitReconciliationLimit):
		}

		p.mu.Lock()
		forcing := p.forcing
		p.mu.Unlock()
		if forcing {
			<-p.background.Done()
			return p.background.Err()
		}

		networkErr := p.network.Err()
		if networkErr == nil {
			networkErr = fmt.Errorf(
				"pasta exited while the private sidecar was running",
			)
		}
		killCtx, cancel := context.WithTimeout(
			context.Background(),
			sidecarCompanionShutdownLimit,
		)
		p.logger.DebugError(
			"kill sidecar after Pasta exited",
			p.background.Kill(killCtx),
		)
		cancel()
		<-p.background.Done()
		p.logger.DebugError(
			"background sidecar result after Pasta failure",
			p.background.Err(),
		)
		return networkErr
	}
}

func (p *Process) finishNetwork() {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		sidecarCompanionShutdownLimit,
	)
	defer cancel()
	p.logger.DebugError(
		"stop Pasta after sidecar exit",
		p.network.Stop(ctx),
	)

	select {
	case <-p.network.Done():
		p.logger.DebugError(
			"Pasta result after sidecar exit",
			p.network.Err(),
		)
	case <-ctx.Done():
		killCtx, killCancel := context.WithTimeout(
			context.Background(),
			sidecarCompanionShutdownLimit,
		)
		defer killCancel()
		p.logger.DebugError(
			"kill Pasta after sidecar exit",
			p.network.Kill(killCtx),
		)
		select {
		case <-p.network.Done():
			p.logger.DebugError(
				"Pasta result after forced sidecar cleanup",
				p.network.Err(),
			)
		case <-killCtx.Done():
			p.logger.DebugError(
				"wait for Pasta after forced sidecar cleanup",
				killCtx.Err(),
			)
		}
	}
}

func cleanupNetworkAfterStartFailure(
	network pasta.Process,
	logger *diagnostic.Logger,
) {
	if network == nil {
		return
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		sidecarCompanionShutdownLimit,
	)
	defer cancel()
	logger.DebugError(
		"kill Pasta after sidecar startup failure",
		network.Kill(ctx),
	)
	select {
	case <-network.Done():
		logger.DebugError(
			"Pasta result after sidecar startup failure",
			network.Err(),
		)
	case <-ctx.Done():
		logger.DebugError(
			"wait for Pasta after sidecar startup failure",
			ctx.Err(),
		)
	}
}
