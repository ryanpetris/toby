package sidecar

// Launches fresh stdio processes from one immutable image and retained exact
// mount set, then bounds termination attempts and joins reap after connector
// loss.

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway/localstdio"
	"petris.dev/toby/internal/sandbox/bwrap"
)

type stdioPrepared struct {
	provider   Provider
	grace      time.Duration
	definition Definition
	metadata   Metadata
	mounts     *MountCapabilities
	logger     *diagnostic.Logger

	mu     sync.Mutex
	closed bool
}

var _ localstdio.PreparedLaunch = (*stdioPrepared)(nil)

// Serve binds the connector directly to one fresh process's stdin and stdout
// while keeping stderr on its independent drain.
func (p *stdioPrepared) Serve(
	ctx context.Context,
	stream io.ReadWriteCloser,
) error {
	if p == nil || p.provider == nil || p.mounts == nil {
		return fmt.Errorf("prepared stdio sidecar is not configured")
	}
	if ctx == nil {
		return fmt.Errorf("stdio sidecar context is nil")
	}
	if stream == nil {
		return fmt.Errorf("stdio sidecar stream is nil")
	}

	prepared, err := p.prepare(ctx)
	if err != nil {
		return err
	}
	process, err := prepared.Start(ctx, bwrap.ProcessIO{
		Stdin:  stream,
		Stdout: stream,
	}, false)
	if err != nil {
		p.logger.DebugError(
			"close prepared stdio sidecar after startup failure",
			prepared.Close(),
		)
		return err
	}

	select {
	case <-process.Done():
		return process.Err()
	case <-ctx.Done():
		p.stop(process)
		return ctx.Err()
	}
}

func (p *stdioPrepared) prepare(
	ctx context.Context,
) (*Prepared, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, fmt.Errorf("prepared stdio sidecar is closed")
	}

	prepared, err := p.provider.PreparePinned(
		ctx,
		cloneDefinition(p.definition),
		p.mounts,
		nil,
	)
	if err != nil {
		return nil, err
	}
	metadata := prepared.Metadata()
	if metadata.ImmutableImage != p.metadata.ImmutableImage ||
		metadata.ManifestDigest != p.metadata.ManifestDigest ||
		metadata.RootFSDigest != p.metadata.RootFSDigest ||
		metadata.Workdir != p.metadata.Workdir {
		p.logger.DebugError(
			"close mismatched prepared stdio sidecar",
			prepared.Close(),
		)
		return nil, fmt.Errorf(
			"stdio sidecar immutable image identity changed",
		)
	}

	return prepared, nil
}

func (p *stdioPrepared) stop(process *Process) {
	stopCtx, cancelStop := context.WithTimeout(
		context.Background(),
		p.grace,
	)
	stopErr := process.Stop(stopCtx)
	cancelStop()

	timer := time.NewTimer(p.grace)
	defer timer.Stop()
	select {
	case <-process.Done():
		p.logger.DebugError("stop stdio sidecar", stopErr)
		p.logger.DebugError(
			"reap stopped stdio sidecar",
			process.Err(),
		)
		return
	case <-timer.C:
	}

	killCtx, cancelKill := context.WithTimeout(
		context.Background(),
		p.grace,
	)
	killErr := process.Kill(killCtx)
	cancelKill()
	killTimer := time.NewTimer(p.grace)
	defer killTimer.Stop()
	select {
	case <-process.Done():
		p.logger.DebugError("stop stdio sidecar", stopErr)
		p.logger.DebugError("kill stdio sidecar", killErr)
		p.logger.DebugError(
			"reap killed stdio sidecar",
			process.Err(),
		)
		return
	case <-killTimer.C:
	}

	// Keep Serve, and therefore the localstdio acquisition that owns it,
	// registered until this exact process has been reaped and its overlay,
	// image, and prepared capabilities have all been released. Release and
	// resolver shutdown remain bounded by their caller contexts while their
	// background join continues to own this work.
	<-process.Done()
	p.logger.DebugError("stop stdio sidecar", stopErr)
	p.logger.DebugError("kill stdio sidecar", killErr)
	p.logger.DebugError(
		"reap stdio sidecar after termination grace",
		process.Err(),
	)
}

// Close releases the target's immutable mount capabilities.
func (p *stdioPrepared) Close() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true

	err := p.mounts.Close()
	p.mounts = nil
	p.definition = Definition{}
	p.metadata = Metadata{}

	return err
}
