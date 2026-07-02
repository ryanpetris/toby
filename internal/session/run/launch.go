package run

// renderGeneratedConfig re-renders the generated tool config + instruction files for a
// toolset. Tool config is written to each tool's real home path (overwritten in place);
// only the Toby-owned artifact dirs (instructions, scripts) are wiped first so stale
// entries don't accumulate.

import (
	"context"
	"fmt"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/tools"
)

func renderGeneratedConfig(ctx context.Context, params Params, toolset *tools.Toolset, lctx lifecycle.Context) error {
	if params.ContextFiles == nil {
		return fmt.Errorf("context files service is not configured")
	}
	for _, dir := range []string{layout.Instructions, layout.Scripts} {
		if err := params.SandboxService.DeletePath(ctx, dir, true); err != nil {
			return err
		}
	}
	return params.Runner.RunPhase(ctx, lifecycle.PhaseContextFiles, toolset, lctx, false)
}
