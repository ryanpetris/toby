package remotehttp

// Binds each connector to a fresh bridge session and cancels every session
// immediately when its run target is revoked.

import (
	"context"
	"fmt"
	"io"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/connector"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
)

type prepared struct {
	bridge   Bridge
	upstream httpbridge.Upstream
	logger   *diagnostic.Logger
}

var _ mcpgateway.PreparedBackend = (*prepared)(nil)

func (p *prepared) Acquire(
	ctx context.Context,
	_ mcpgateway.ProgressReporter,
) (mcpgateway.AcquiredBackend, error) {
	if p == nil || p.bridge == nil {
		return nil, fmt.Errorf("prepared remote MCP target is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("acquire remote MCP target context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())
	return &acquired{
		bridge: p.bridge,
		upstream: httpbridge.Upstream{
			Endpoint: p.upstream.Endpoint,
			Headers:  p.upstream.Headers.Clone(),
		},
		lifetime: lifetime,
		cancel:   cancel,
		logger:   p.logger,
	}, nil
}

type acquired struct {
	bridge   Bridge
	upstream httpbridge.Upstream
	lifetime context.Context
	cancel   context.CancelFunc
	logger   *diagnostic.Logger
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
	sessionCtx, cancel := mcpgateway.MergeContexts(ctx, a.lifetime)
	defer cancel()

	if err := a.bridge.Serve(
		sessionCtx,
		conn,
		httpbridge.Upstream{
			Endpoint: a.upstream.Endpoint,
			Headers:  a.upstream.Headers.Clone(),
		},
	); err != nil && sessionCtx.Err() == nil {
		a.logger.DebugError("serve remote HTTP MCP connection", err)
	}
}

func (a *acquired) Revoke() {
	if a == nil || a.cancel == nil {
		return
	}

	a.cancel()
}

func (a *acquired) Release(context.Context) error {
	a.Revoke()
	return nil
}
