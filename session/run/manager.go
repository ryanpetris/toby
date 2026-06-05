package run

// Driving the in-sandbox Toby manager process: sandboxManagerArgv builds its
// bootstrap argv, runMountInit runs the mount-setup pass and startRunSandboxManager
// the launch pass (each waiting on readiness vs. early exit), and the terminate/
// wait helpers shut the manager down and surface its exit code.

import (
	"context"

	"petris.dev/toby/control/host"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/lifecycle"
	sandbox "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

type sandboxManagerReady struct {
	client *host.SandboxClient
	err    error
}

func sandboxManagerArgv(sbx sandbox.Instance) []string {
	return []string{
		"/bin/sh", "-c",
		`set -e; mkdir -p "$1"; curl -fsSL -H "Authorization: Bearer ${TOBY_CONTROL_TOKEN:?}" "http://${TOBY_CONTROL_HOST:?}/binary" -o "$2"; chmod 755 "$2"; exec "$2" sandbox manager`,
		"toby-startup", sbx.TobyBinDir(), sbx.TobyBinaryPath(),
	}
}

func runMountInit(ctx context.Context, params Params, manager *host.Service, sbx sandbox.Instance, spec sandbox.RunSpec) error {
	exits := sandbox.NewCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	managerExit := sandbox.NewManagerExit()
	manager.CommandExit = exits.Complete
	manager.ContextInit = func(ctx context.Context, client *host.SandboxClient) error {
		if err := params.SandboxService.Connect(ctx, sbx, client, exits, managerExit); err != nil {
			return err
		}
		return params.SandboxService.MountSetup(ctx)
	}
	manager.SandboxReady = func(client *host.SandboxClient, err error) {
		ready <- sandboxManagerReady{client: client, err: err}
	}
	go func() {
		code, err := sbx.Setup(ctx, spec)
		managerExit.Set(sandbox.ProcessResult{ExitCode: code, Err: err})
	}()
	select {
	case result := <-ready:
		if result.err != nil {
			return waitSandboxManagerAfterError(ctx, managerExit, result.client, result.err)
		}
		return terminateSandboxManager(ctx, result.client, managerExit)
	case <-managerExit.Done():
		result := managerExit.Result()
		if result.Err != nil {
			return result.Err
		}
		return exitcode.New(result.ExitCode, "sandbox setup manager exited before context init")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func startRunSandboxManager(ctx context.Context, params Params, manager *host.Service, sbx sandbox.Instance, opts *tools.Options, spec sandbox.RunSpec, toolset *tools.Toolset, lctx lifecycle.Context) (*host.SandboxClient, *sandbox.ManagerExit, error) {
	exits := sandbox.NewCommandExits()
	ready := make(chan sandboxManagerReady, 1)
	managerExit := sandbox.NewManagerExit()
	manager.CommandExit = exits.Complete
	manager.ContextInit = func(ctx context.Context, client *host.SandboxClient) error {
		if err := params.SandboxService.Connect(ctx, sbx, client, exits, managerExit); err != nil {
			return err
		}
		if err := params.Runner.RunPhase(ctx, lifecycle.PhaseConfigureSandbox, toolset, lctx, false); err != nil {
			return err
		}
		return initSandboxContext(ctx, params, toolset, lctx)
	}
	manager.SandboxReady = func(client *host.SandboxClient, err error) {
		ready <- sandboxManagerReady{client: client, err: err}
	}
	go func() {
		code, err := sbx.Run(ctx, spec)
		managerExit.Set(sandbox.ProcessResult{ExitCode: code, Err: err})
	}()
	select {
	case result := <-ready:
		if result.err != nil {
			return nil, nil, waitSandboxManagerAfterError(ctx, managerExit, result.client, result.err)
		}
		return result.client, managerExit, nil
	case <-managerExit.Done():
		result := managerExit.Result()
		if result.Err != nil {
			return nil, nil, result.Err
		}
		return nil, nil, exitcode.New(result.ExitCode, "sandbox manager exited before context init")
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

func waitSandboxManagerAfterError(ctx context.Context, exit *sandbox.ManagerExit, client *host.SandboxClient, err error) error {
	_ = terminateSandboxManager(ctx, client, exit)
	return err
}

func terminateSandboxManager(ctx context.Context, client *host.SandboxClient, exit *sandbox.ManagerExit) error {
	if client != nil {
		if err := client.Terminate(ctx); err != nil {
			return err
		}
	}
	select {
	case <-exit.Done():
		result := exit.Result()
		if result.Err != nil {
			return result.Err
		}
		if result.ExitCode != 0 {
			return exitcode.Code(result.ExitCode)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
