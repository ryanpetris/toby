package tool

import (
	"context"

	"go.uber.org/fx"
)

type LifecycleHooksResult struct {
	fx.Out

	HostInit     []LifecycleHook `group:"toby.lifecycle.host.init,flatten"`
	MountInit    []LifecycleHook `group:"toby.lifecycle.sandbox.mount.init,flatten"`
	ContextSetup []LifecycleHook `group:"toby.lifecycle.sandbox.context.setup,flatten"`
	ContextInit  []LifecycleHook `group:"toby.lifecycle.sandbox.context.init,flatten"`
	SandboxInit  []LifecycleHook `group:"toby.lifecycle.sandbox.init,flatten"`
	Install      []LifecycleHook `group:"toby.lifecycle.sandbox.install,flatten"`
	Upgrade      []LifecycleHook `group:"toby.lifecycle.sandbox.upgrade,flatten"`
}

func NewLifecycleHooks(params RegistryParams) LifecycleHooksResult {
	var result LifecycleHooksResult
	for _, item := range params.Tools {
		priority := item.LifecyclePriority()
		name := "tool." + item.Name()
		owner := item.Name()
		result.HostInit = append(result.HostInit, LifecycleHook{Name: name, Owner: owner, Priority: priority, Run: func(item Tool) func(context.Context, LifecycleContext) error {
			return func(ctx context.Context, lifecycleCtx LifecycleContext) error {
				return item.HostInit(ctx, lifecycleCtx.Options)
			}
		}(item)})
		result.ContextSetup = append(result.ContextSetup, LifecycleHook{Name: name, Owner: owner, Priority: priority, Run: func(item Tool) func(context.Context, LifecycleContext) error {
			return func(ctx context.Context, _ LifecycleContext) error { return item.SandboxContextSetup(ctx) }
		}(item)})
		if registrar, ok := item.(ContextFileTool); ok {
			result.ContextInit = append(result.ContextInit, LifecycleHook{Name: name, Owner: owner, Priority: priority, Run: func(registrar ContextFileTool) func(context.Context, LifecycleContext) error {
				return func(ctx context.Context, lifecycleCtx LifecycleContext) error {
					var opts ContextOptions
					if lifecycleCtx.Options != nil {
						opts.SuppressWarnings = lifecycleCtx.Options.SuppressWarnings
					}
					opts.Stderr = lifecycleCtx.Stderr
					return registrar.RegisterContextFiles(ctx, opts)
				}
			}(registrar)})
		}
		result.SandboxInit = append(result.SandboxInit, LifecycleHook{Name: name, Owner: owner, Priority: priority, Run: func(item Tool) func(context.Context, LifecycleContext) error {
			return func(ctx context.Context, _ LifecycleContext) error { return item.SandboxInit(ctx) }
		}(item)})
		result.Install = append(result.Install, LifecycleHook{Name: name, Owner: owner, Priority: priority, Run: func(item Tool) func(context.Context, LifecycleContext) error {
			return func(ctx context.Context, _ LifecycleContext) error { return item.Install(ctx) }
		}(item)})
		result.Upgrade = append(result.Upgrade, LifecycleHook{Name: name, Owner: owner, Priority: priority, Run: func(item Tool) func(context.Context, LifecycleContext) error {
			return func(ctx context.Context, _ LifecycleContext) error { return item.Upgrade(ctx) }
		}(item)})
	}
	return result
}
