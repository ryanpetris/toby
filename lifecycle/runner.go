package lifecycle

// The Runner drives a Toolset through the launch phases, merging each tool's
// phase method with the registered non-tool hooks for that phase.

import (
	"context"
	"sort"

	"go.uber.org/fx"

	"petris.dev/toby/tools"
)

// Runner drives a Toolset through the lifecycle phases, merging each tool's phase
// method with the registered non-tool Hooks for that phase.
type Runner struct {
	hooks []Hook
}

// NewRunner builds a Runner from the Hooks registered into the fx group.
func NewRunner(hooks []Hook) *Runner {
	return &Runner{hooks: append([]Hook(nil), hooks...)}
}

// Module provides the Runner, consuming every Hook in the "toby.lifecycle" group.
func Module() fx.Option {
	return fx.Module("lifecycle",
		fx.Provide(
			fx.Annotate(NewRunner, fx.ParamTags(`group:"toby.lifecycle"`)),
		),
	)
}

type action struct {
	priority int
	name     string
	run      func(context.Context) error
}

// RunPhase runs phase for the toolset: every active tool's phase method plus
// every Hook registered for the phase, ordered by priority then name. force is
// passed to PhaseInstall (true performs an upgrade).
func (r *Runner) RunPhase(ctx context.Context, phase Phase, set *tools.Toolset, lctx Context, force bool) error {
	var actions []action
	if set != nil {
		for _, t := range set.OrderedTools() {
			run := toolAction(t, phase, lctx, force)
			if run == nil {
				continue
			}
			actions = append(actions, action{priority: t.LifecyclePriority(), name: t.Name(), run: run})
		}
	}
	for _, hook := range r.hooks {
		if hook.Phase != phase || hook.Run == nil {
			continue
		}
		hook := hook
		actions = append(actions, action{priority: hook.Priority, name: hook.Name, run: func(ctx context.Context) error {
			return hook.Run(ctx, lctx)
		}})
	}
	sort.SliceStable(actions, func(i, j int) bool {
		if actions[i].priority == actions[j].priority {
			return actions[i].name < actions[j].name
		}
		return actions[i].priority < actions[j].priority
	})
	for _, a := range actions {
		if err := a.run(ctx); err != nil {
			return err
		}
	}
	return nil
}

// toolAction returns the tool's behavior for phase, or nil if the tool does not
// participate in it (e.g. a tool that does not register context files).
func toolAction(t tools.Tool, phase Phase, lctx Context, force bool) func(context.Context) error {
	switch phase {
	case PhaseHostPrepare:
		return func(ctx context.Context) error { return t.PrepareHost(ctx, lctx.Options) }
	case PhaseConfigureSandbox:
		return func(ctx context.Context) error { return t.ConfigureSandbox(ctx) }
	case PhaseContextFiles:
		registrar, ok := t.(tools.ContextFileRegistrar)
		if !ok {
			return nil
		}
		return func(ctx context.Context) error {
			opts := tools.ContextOptions{Stderr: lctx.Stderr}
			if lctx.Options != nil {
				opts.SuppressWarnings = lctx.Options.SuppressWarnings
			}
			return registrar.RegisterContextFiles(ctx, opts)
		}
	case PhaseInitSandbox:
		return func(ctx context.Context) error { return t.InitSandbox(ctx) }
	case PhaseInstall:
		return func(ctx context.Context) error { return t.Install(ctx, force) }
	default:
		return nil
	}
}
