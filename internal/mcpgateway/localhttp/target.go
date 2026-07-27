package localhttp

// Owns one service lease on a shared HTTP process and pins the exact generation for
// every fresh logical MCP session.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/progressio"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/connector"
)

const defaultCleanupTimeout = 15 * time.Second

type prepared struct {
	bridge     Bridge
	pool       Pool
	definition Definition
	logger     *diagnostic.Logger
}

var _ mcpgateway.PreparedBackend = (*prepared)(nil)

func (p *prepared) Acquire(
	ctx context.Context,
	progress mcpgateway.ProgressReporter,
) (mcpgateway.AcquiredBackend, error) {
	if p == nil || p.bridge == nil || p.pool == nil {
		return nil, fmt.Errorf("prepared local HTTP MCP target is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("acquire local HTTP MCP target context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	prepareOperation := progressio.Start(
		progress,
		mcpgateway.ResourceKind,
		"Preparing local HTTP MCP image",
	)
	preparedService, err := p.pool.Prepare(
		ctx,
		p.definition.clone(),
		progress,
	)
	if err != nil {
		prepareOperation.Fail("Preparing local HTTP MCP image")
		return nil, fmt.Errorf(
			"prepare local HTTP MCP process: %w",
			err,
		)
	}
	if preparedService == nil {
		prepareOperation.Fail("Preparing local HTTP MCP image")
		return nil, fmt.Errorf(
			"prepare local HTTP MCP process: pool returned nil",
		)
	}
	prepareOperation.Complete("Prepared local HTTP MCP image")

	startOperation := progressio.Start(
		progress,
		mcpgateway.ResourceKind,
		"Starting local HTTP MCP sidecar",
	)
	service, err := preparedService.Acquire(ctx)
	p.logger.DebugError(
		"close prepared local HTTP MCP service",
		preparedService.Close(),
	)
	if err != nil {
		startOperation.Fail("Starting local HTTP MCP sidecar")
		return nil, fmt.Errorf(
			"acquire local HTTP MCP process: %w",
			err,
		)
	}
	if service == nil {
		startOperation.Fail("Starting local HTTP MCP sidecar")
		return nil, fmt.Errorf(
			"acquire local HTTP MCP process: pool returned nil lease",
		)
	}
	startOperation.Complete("Started local HTTP MCP sidecar")

	lifetime, cancel := context.WithCancel(context.Background())
	return &acquired{
		bridge:    p.bridge,
		service:   service,
		lifetime:  lifetime,
		cancel:    cancel,
		accepting: true,
		done:      make(chan struct{}),
		logger:    p.logger,
	}, nil
}

type acquired struct {
	bridge  Bridge
	service ServiceLease

	lifetime context.Context
	cancel   context.CancelFunc

	mu        sync.Mutex
	accepting bool
	wait      sync.WaitGroup
	done      chan struct{}
	err       error
	logger    *diagnostic.Logger

	revokeOnce  sync.Once
	releaseOnce sync.Once
}

var _ mcpgateway.AcquiredBackend = (*acquired)(nil)
var _ connector.Target = (*acquired)(nil)

func (a *acquired) Target() connector.Target {
	return a
}

func (a *acquired) ServeConnector(
	ctx context.Context,
	conn io.ReadWriteCloser,
) {
	a.mu.Lock()
	if !a.accepting {
		a.mu.Unlock()
		return
	}
	a.wait.Add(1)
	a.mu.Unlock()
	defer a.wait.Done()

	sessionCtx, cancelSession := mergeContexts(ctx, a.lifetime)
	defer cancelSession()

	generation, err := a.service.OpenConnector(sessionCtx)
	if err != nil || generation == nil {
		return
	}
	defer generation.Close()

	upstream, err := generation.Upstream()
	if err != nil {
		return
	}

	bridgeCtx, cancelBridge := context.WithCancel(sessionCtx)
	generationDone := generation.Done()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-bridgeCtx.Done():
		case <-generationDone:
			cancelBridge()
		}
	}()

	serveErr := a.bridge.Serve(
		bridgeCtx,
		conn,
		cloneUpstream(upstream),
	)
	cancelBridge()
	<-watchDone
	if err := errors.Join(serveErr, generation.Err()); err != nil &&
		sessionCtx.Err() == nil {
		a.logger.DebugError("serve local HTTP MCP connection", err)
	}
}

func (a *acquired) Revoke() {
	if a == nil {
		return
	}

	a.revokeOnce.Do(func() {
		a.mu.Lock()
		a.accepting = false
		a.mu.Unlock()

		a.cancel()
		a.service.Revoke()
	})
}

func (a *acquired) Release(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	a.Revoke()
	a.releaseOnce.Do(func() {
		go func() {
			a.wait.Wait()
			releaseCtx, cancel := context.WithTimeout(
				context.Background(),
				defaultCleanupTimeout,
			)
			releaseErr := a.service.Release(releaseCtx)
			cancel()

			a.mu.Lock()
			a.err = errors.Join(a.err, releaseErr)
			a.mu.Unlock()
			close(a.done)
		}()
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.err
	}
}

func mergeContexts(
	first context.Context,
	second context.Context,
) (context.Context, context.CancelFunc) {
	if first == nil {
		first = context.Background()
	}
	if second == nil {
		second = context.Background()
	}

	ctx, cancel := context.WithCancel(first)
	stop := context.AfterFunc(second, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}
