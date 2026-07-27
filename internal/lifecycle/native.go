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

// Native drives lifecycle ordering for the Bubblewrap architecture while the
// existing Runner remains responsible for hook/tool ordering inside a phase.
type Native struct {
	runner *Runner
}

// NewNative creates a native lifecycle coordinator.
func NewNative(runner *Runner) *Native {
	return &Native{runner: runner}
}

// PrepareHost collects host-side mount and bind declarations. The launch
// orchestrator resolves storage and its agent session after this method and
// before Configure, so tools never render files from an empty session.
func (n *Native) PrepareHost(
	ctx context.Context,
	set *tools.Toolset,
	lctx Context,
) error {
	if n == nil || n.runner == nil {
		return fmt.Errorf("native lifecycle runner is not configured")
	}
	if set == nil || set.Primary() == nil {
		return fmt.Errorf("native lifecycle requires a primary tool")
	}

	return n.runner.RunPhase(
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
func (n *Native) Configure(
	ctx context.Context,
	set *tools.Toolset,
	lctx Context,
	primaryArgs []string,
) error {
	if n == nil || n.runner == nil {
		return fmt.Errorf("native lifecycle runner is not configured")
	}
	if set == nil || set.Primary() == nil {
		return fmt.Errorf("native lifecycle requires a primary tool")
	}
	if err := n.runner.RunPhase(
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
func (n *Native) Initialize(
	ctx context.Context,
	set *tools.Toolset,
	lctx Context,
	force bool,
) error {
	if n == nil || n.runner == nil {
		return fmt.Errorf("native lifecycle runner is not configured")
	}
	if set == nil {
		return fmt.Errorf("native lifecycle requires a toolset")
	}
	if err := n.runner.RunPhase(
		ctx,
		PhaseInitSandbox,
		set,
		lctx,
		false,
	); err != nil {
		return err
	}
	return n.runner.RunPhase(ctx, PhaseInstall, set, lctx, force)
}

// Launch executes the selected primary tool after initialization.
func (n *Native) Launch(
	ctx context.Context,
	set *tools.Toolset,
	args []string,
) error {
	if n == nil || n.runner == nil {
		return fmt.Errorf("native lifecycle runner is not configured")
	}
	if set == nil || set.Primary() == nil {
		return fmt.Errorf("native lifecycle requires a primary tool")
	}

	return set.Primary().Launch(ctx, append([]string(nil), args...))
}
