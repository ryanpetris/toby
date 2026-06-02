package tool

import (
	"context"
	"io"
	"sort"
)

const (
	FxLifecycleHostInit            = "toby.lifecycle.host.init"
	FxLifecycleSandboxMountInit    = "toby.lifecycle.sandbox.mount.init"
	FxLifecycleSandboxContextSetup = "toby.lifecycle.sandbox.context.setup"
	FxLifecycleSandboxContextInit  = "toby.lifecycle.sandbox.context.init"
	FxLifecycleSandboxInit         = "toby.lifecycle.sandbox.init"
	FxLifecycleSandboxInstall      = "toby.lifecycle.sandbox.install"
	FxLifecycleSandboxUpgrade      = "toby.lifecycle.sandbox.upgrade"
)

type LifecycleContext struct {
	Options *CommandOptions
	Stderr  io.Writer
}

type LifecycleHook struct {
	Name     string
	Owner    string
	Priority int
	Run      func(context.Context, LifecycleContext) error
}

func RunLifecycle(ctx context.Context, hooks []LifecycleHook, active []string, lifecycleCtx LifecycleContext) error {
	activeSet := map[string]bool{}
	for _, name := range active {
		activeSet[name] = true
	}
	items := make([]LifecycleHook, 0, len(hooks))
	for _, hook := range hooks {
		if hook.Run == nil {
			continue
		}
		if hook.Owner != "" && !activeSet[hook.Owner] {
			continue
		}
		items = append(items, hook)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Priority == items[j].Priority {
			return items[i].Name < items[j].Name
		}
		return items[i].Priority < items[j].Priority
	})
	for _, hook := range items {
		if err := hook.Run(ctx, lifecycleCtx); err != nil {
			return err
		}
	}
	return nil
}
