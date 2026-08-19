package builtin

// Connects one acquired built-in target to fresh MCP SDK IO transports and
// revokes all of its live sessions together with the run.

import (
	"context"
	"fmt"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/connector"
	"petris.dev/toby/internal/tobymcp"
	gitservice "petris.dev/toby/internal/tobymcp/services/git"
)

type prepared struct {
	runner   *tobymcp.Runner
	caller   agentserver.HostActionCaller
	snapshot tobymcp.SessionSnapshot
	logger   *diagnostic.Logger
}

var _ mcpgateway.PreparedBackend = (*prepared)(nil)

func (p *prepared) Acquire(
	ctx context.Context,
	_ mcpgateway.ProgressReporter,
) (mcpgateway.AcquiredBackend, error) {
	if p == nil || p.runner == nil || p.caller == nil {
		return nil, fmt.Errorf("prepared built-in MCP target is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("acquire built-in MCP context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())
	return &acquired{
		runner:   p.runner,
		caller:   p.caller,
		snapshot: p.snapshot.Clone(),
		logger:   p.logger,
		lifetime: lifetime,
		cancel:   cancel,
	}, nil
}

type acquired struct {
	runner   *tobymcp.Runner
	caller   agentserver.HostActionCaller
	snapshot tobymcp.SessionSnapshot
	logger   *diagnostic.Logger

	lifetime context.Context
	cancel   context.CancelFunc
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

	transport := &mcp.IOTransport{Reader: conn, Writer: conn}
	if err := a.runner.Serve(
		sessionCtx,
		gitservice.NewReverseGitClient(a.caller),
		a.snapshot,
		transport,
	); err != nil && sessionCtx.Err() == nil {
		a.logger.DebugError("serve built-in MCP connection", err)
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
