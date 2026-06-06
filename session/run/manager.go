package run

// Driving the single sandbox container: startRunSandbox creates and starts it
// (binary delivered via docker cp), serves the Tunnel gRPC over its stdio link,
// waits for the in-sandbox manager's Ready, then runs mount-init, sandbox
// configuration, and the requested tool — all via docker exec/cp — and tears the
// container down on return.

import (
	"context"
	"fmt"
	"time"

	"petris.dev/toby/control/host"
	"petris.dev/toby/control/stdio"
	"petris.dev/toby/control/tunnel"
	"petris.dev/toby/lifecycle"
	"petris.dev/toby/platform/environ"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"

	"google.golang.org/grpc"
)

// readyTimeout bounds how long we wait for the in-sandbox manager to bind its
// proxy listener and report Ready after the container starts.
const readyTimeout = 30 * time.Second

func startRunSandbox(ctx context.Context, params Params, manager *host.Service, sbx sandbox.Instance, env environ.Environment, runSpec sandbox.RunSpec, toolset *tools.Toolset, lctx lifecycle.Context, opts *tools.Options, extra []string) error {
	if manager == nil || manager.HTTPProxy == nil {
		return fmt.Errorf("http proxy service is not configured")
	}
	debug := params.TobyConfig.DebugEnabled()

	conn, err := sbx.RunStart(ctx, runSpec)
	if err != nil {
		return err
	}
	defer sbx.RunStop(ctx, debug)
	defer conn.Close()

	// Serve the Tunnel gRPC service over the container's stdio link. The manager is
	// the client; it calls Ready once its local proxy listener is bound.
	ready := make(chan string, 1)
	tunnelSrv := tunnel.NewServer(manager.HTTPProxy, func(addr string) {
		select {
		case ready <- addr:
		default:
		}
	})
	grpcSrv := grpc.NewServer()
	tunnel.RegisterTunnelServer(grpcSrv, tunnelSrv)
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = grpcSrv.Serve(stdio.NewListener(conn))
	}()
	defer func() {
		grpcSrv.Stop()
		_ = tunnelSrv.Close()
	}()

	select {
	case <-ready:
	case <-serveDone:
		return fmt.Errorf("sandbox manager exited before reporting ready")
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(readyTimeout):
		return fmt.Errorf("timed out waiting for sandbox manager to start")
	}

	if err := params.SandboxService.BindRun(ctx, sbx, env); err != nil {
		return err
	}
	// Mount-init: chown the provider volumes at their setup paths (root docker exec).
	if err := params.SandboxService.MountSetup(ctx); err != nil {
		return err
	}
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseConfigureSandbox, toolset, lctx, false); err != nil {
		return err
	}
	if err := initSandboxContext(ctx, params, toolset, lctx); err != nil {
		return err
	}
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInitSandbox, toolset, lctx, false); err != nil {
		return err
	}
	return launchTool(ctx, params, toolset, opts, extra, lctx)
}
