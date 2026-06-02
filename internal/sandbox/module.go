package sandbox

import (
	"context"

	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

func Module() fx.Option {
	return fx.Module(
		"sandbox",
		fx.Provide(
			sandboxmount.NewService,
			newService,
			func(s *Service) tool.SandboxService { return s },
			provideLifecycleHooks,
			provideFactory,
		),
	)
}

type lifecycleHooksResult struct {
	fx.Out

	HostInit tool.LifecycleHook `group:"toby.lifecycle.host.init"`
}

func provideLifecycleHooks(mounts *sandboxmount.Service) lifecycleHooksResult {
	return lifecycleHooksResult{
		HostInit: tool.LifecycleHook{
			Name:     "sandbox.mounts.prepare-host",
			Priority: 10000,
			Run: func(context.Context, tool.LifecycleContext) error {
				return mounts.PrepareHostMounts()
			},
		},
	}
}
