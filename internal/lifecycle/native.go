package lifecycle

// Orders the retained tool lifecycle around native Bubblewrap run assembly,
// leaving an explicit boundary for run-scoped session resolution.

import (
	"context"
	"fmt"

	"petris.dev/toby/internal/tools"
)

// LaunchPreparer is an optional concrete-tool capability for deriving
// per-launch environment from the primary command arguments before the run
// plan freezes.
type LaunchPreparer interface {
	// PrepareLaunch derives launch declarations from foreground arguments.
	PrepareLaunch(context.Context, []string) error
}

// PrepareHost collects host-side mount and bind declarations. The launch
// orchestrator resolves storage and its agent session after this method and
// before Configure, so tools never render files from an empty session.
func (r *Runner) PrepareHost(
	ctx context.Context,
	set *tools.Toolset,
	lctx Context,
) error {
	if r == nil {
		return fmt.Errorf("lifecycle runner is not configured")
	}
	if set == nil || set.Primary() == nil {
		return fmt.Errorf("native lifecycle requires a primary tool")
	}

	return r.RunPhase(
		ctx,
		PhaseHostPrepare,
		set,
		lctx,
		false,
	)
}

// Configure fixes tool environment and argument-dependent launch declarations
// after the orchestrator has populated run-scoped session configuration, but
// before the Bubblewrap plan is assembled and attached.
func (r *Runner) Configure(
	ctx context.Context,
	set *tools.Toolset,
	lctx Context,
	primaryArgs []string,
) error {
	if r == nil {
		return fmt.Errorf("lifecycle runner is not configured")
	}
	if set == nil || set.Primary() == nil {
		return fmt.Errorf("native lifecycle requires a primary tool")
	}
	if err := r.RunPhase(
		ctx,
		PhaseConfigureSandbox,
		set,
		lctx,
		false,
	); err != nil {
		return err
	}

	preparer, ok := set.Primary().(LaunchPreparer)
	if !ok {
		return nil
	}
	return preparer.PrepareLaunch(
		ctx,
		append([]string(nil), primaryArgs...),
	)
}

// Initialize runs every in-sandbox initialization command and then the install
// phase against the already attached shared Bubblewrap run.
func (r *Runner) Initialize(
	ctx context.Context,
	set *tools.Toolset,
	lctx Context,
	force bool,
) error {
	if r == nil {
		return fmt.Errorf("lifecycle runner is not configured")
	}
	if set == nil {
		return fmt.Errorf("native lifecycle requires a toolset")
	}
	if err := r.RunPhase(
		ctx,
		PhaseInitSandbox,
		set,
		lctx,
		false,
	); err != nil {
		return err
	}
	return r.RunPhase(ctx, PhaseInstall, set, lctx, force)
}

// Launch executes the selected primary tool after initialization.
func (r *Runner) Launch(
	ctx context.Context,
	set *tools.Toolset,
	args []string,
) error {
	if r == nil {
		return fmt.Errorf("lifecycle runner is not configured")
	}
	if set == nil || set.Primary() == nil {
		return fmt.Errorf("native lifecycle requires a primary tool")
	}

	return set.Primary().Launch(ctx, append([]string(nil), args...))
}
