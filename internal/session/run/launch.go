package run

// Running the requested tool inside the prepared sandbox: initSandboxContext
// clears and re-renders the generated context files, and launchTool drives the
// install/upgrade phases before handing control to the primary tool.

import (
	"context"
	"fmt"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/tools"
)

func initSandboxContext(ctx context.Context, params Params, toolset *tools.Toolset, lctx lifecycle.Context) error {
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	contextDir := layout.Context
	if err := params.SandboxService.DeletePath(ctx, contextDir, true); err != nil {
		return err
	}
	return params.Runner.RunPhase(ctx, lifecycle.PhaseContextFiles, toolset, lctx, false)
}

func launchTool(ctx context.Context, params Params, toolset *tools.Toolset, opts *tools.Options, extra []string, lctx lifecycle.Context) error {
	primary := toolset.Primary()
	if primary == nil {
		return fmt.Errorf("toolset cannot launch without a primary tool")
	}
	if opts != nil && opts.Install {
		return params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, toolset, lctx, false)
	}
	if opts != nil && opts.Upgrade {
		if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, toolset, lctx, true); err != nil {
			return err
		}
		return primary.Launch(ctx, extra)
	}
	if err := params.Runner.RunPhase(ctx, lifecycle.PhaseInstall, toolset, lctx, false); err != nil {
		return err
	}
	return primary.Launch(ctx, extra)
}
