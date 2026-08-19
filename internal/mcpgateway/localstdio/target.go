package localstdio

// Owns one run target whose every admitted connector launches a separate,
// long-lived stdio process.

import (
	"context"
	"fmt"
	"io"
	"sync"

	"petris.dev/toby/internal/agent/progressio"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/connector"
)

type prepared struct {
	resolver *Resolver
	launch   Launch
}

var _ mcpgateway.PreparedBackend = (*prepared)(nil)

func (p *prepared) Acquire(
	ctx context.Context,
	progress mcpgateway.ProgressReporter,
) (mcpgateway.AcquiredBackend, error) {
	if p == nil || p.resolver == nil || p.resolver.launcher == nil {
		return nil, fmt.Errorf("prepared local stdio MCP target is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("acquire local stdio MCP target context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	operation := progressio.Start(
		progress,
		mcpgateway.ResourceKind,
		"Preparing local stdio MCP image",
	)
	launch, err := p.resolver.launcher.Prepare(
		ctx,
		p.launch.clone(),
		progress,
	)
	if err != nil {
		operation.Fail("Preparing local stdio MCP image")
		return nil, fmt.Errorf(
			"prepare local stdio MCP target: %w",
			err,
		)
	}
	if launch == nil {
		operation.Fail("Preparing local stdio MCP image")
		return nil, fmt.Errorf(
			"prepare local stdio MCP target: launcher returned nil",
		)
	}
	operation.Complete("Prepared local stdio MCP image")

	lifetime, cancel := context.WithCancel(context.Background())
	result := &acquired{
		resolver:  p.resolver,
		launch:    launch,
		lifetime:  lifetime,
		cancel:    cancel,
		accepting: true,
		done:      make(chan struct{}),
	}
	if err := p.resolver.register(result); err != nil {
		cancel()
		p.resolver.logger.DebugError(
			"close local stdio MCP launch after registration failed",
			launch.Close(),
		)
		return nil, err
	}

	return result, nil
}

type acquired struct {
	resolver *Resolver
	launch   PreparedLaunch

	lifetime context.Context
	cancel   context.CancelFunc

	mu        sync.Mutex
	accepting bool
	wait      sync.WaitGroup
	done      chan struct{}

	revokeOnce  sync.Once
	releaseOnce sync.Once
	releaseErr  error
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

	sessionCtx, cancel := mcpgateway.MergeContexts(ctx, a.lifetime)
	defer cancel()

	if err := a.launch.Serve(sessionCtx, conn); err != nil &&
		sessionCtx.Err() == nil {
		a.resolver.logger.DebugError(
			"serve local stdio MCP connection",
			err,
		)
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
			a.releaseErr = a.launch.Close()
			a.resolver.unregister(a)
			close(a.done)
		}()
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		return a.releaseErr
	}
}
