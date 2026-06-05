// Package contextinit contributes Toby's own (non-tool) context-file steps to the
// launch lifecycle: writing the bundled agent instructions and the rendered Toby
// config into the sandbox context directory. They run in the context-files phase
// before tools (negative priority) so tool config can build on them.
package contextinit

import (
	"context"

	"go.uber.org/fx"

	tobyconfig "petris.dev/toby/config/toby"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/lifecycle"
)

// HooksResult registers the context-init hooks into the lifecycle group.
type HooksResult struct {
	fx.Out

	AgentInstructions lifecycle.Hook `group:"lifecycle"`
	TobyConfig        lifecycle.Hook `group:"lifecycle"`
}

// NewLifecycleHooks builds the agent-instructions and toby-config context-file
// hooks.
func NewLifecycleHooks(cfg *tobyconfig.Service, contextFiles *contextfiles.Service) HooksResult {
	return HooksResult{
		AgentInstructions: lifecycle.Hook{
			Phase:    lifecycle.PhaseContextFiles,
			Name:     "context.agent-instructions",
			Priority: -200,
			Run: func(ctx context.Context, _ lifecycle.Context) error {
				_, err := contextFiles.AddInstructionFS(ctx, TobyAgentsPath, AgentFiles(), TobyAgentsPath, 0o644)
				return err
			},
		},
		TobyConfig: lifecycle.Hook{
			Phase:    lifecycle.PhaseContextFiles,
			Name:     "context.toby-config",
			Priority: -100,
			Run: func(ctx context.Context, _ lifecycle.Context) error {
				if cfg == nil {
					return nil
				}
				return cfg.RegisterContextFiles(ctx, contextFiles)
			},
		},
	}
}
