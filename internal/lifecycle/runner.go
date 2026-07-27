package lifecycle

// The Runner drives a Toolset through the launch phases.

import (
	"context"

	"petris.dev/toby/internal/status"
	"petris.dev/toby/internal/tools"
)

// Runner drives a Toolset through the lifecycle phases.
type Runner struct {
	status *status.Service
}

// NewRunner builds a Runner.
func NewRunner(status *status.Service) *Runner {
	return &Runner{status: status}
}

// RunPhase runs every active tool's phase method in the toolset's topological
// order. The force argument is passed to PhaseInstall, where true performs an
// upgrade.
func (r *Runner) RunPhase(ctx context.Context, phase Phase, set *tools.Toolset, lctx Context, force bool) error {
	if set != nil {
		for _, t := range set.OrderedTools() {
			run := toolAction(t, phase, lctx, force)
			if run == nil {
				continue
			}
			actionCtx := ctx
			var operation *status.Operation
			if verb := phaseVerb(phase); verb != "" {
				operation = r.status.StartScopedOperation(
					t.DisplayName(),
					verb,
				)
				actionCtx = withCommandOperation(
					ctx,
					operation,
				)
			}
			err := run(actionCtx)
			operation.Finish(err)
			if err != nil {
				return err
			}
			if lctx.Checkpoint != nil {
				if err := lctx.Checkpoint(); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// phaseVerb is the action shown beneath a tool's display-name scope while a
// phase runs, or "" for phases that have no meaningful per-tool status.
func phaseVerb(phase Phase) string {
	switch phase {
	case PhaseInitSandbox:
		return "Initializing"
	case PhaseInstall:
		return "Installing"
	default:
		return ""
	}
}

// toolAction returns the tool's behavior for phase.
func toolAction(t tools.Tool, phase Phase, lctx Context, force bool) func(context.Context) error {
	switch phase {
	case PhaseHostPrepare:
		return func(ctx context.Context) error { return t.PrepareHost(ctx, lctx.Options) }
	case PhaseConfigureSandbox:
		return func(ctx context.Context) error { return t.ConfigureSandbox(ctx) }
	case PhaseInitSandbox:
		return func(ctx context.Context) error { return t.InitSandbox(ctx) }
	case PhaseInstall:
		return func(ctx context.Context) error { return t.Install(ctx, force) }
	default:
		return nil
	}
}
