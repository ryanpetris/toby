package helpers

import (
	"context"

	"petris.dev/toby/internal/tools/tool"
)

const (
	lifecycleHostInit             = "host-init"
	lifecycleSandboxContextSetup  = "sandbox-context-setup"
	lifecycleRegisterContextFiles = "register-context-files"
	lifecycleSandboxInit          = "sandbox-init"
	lifecycleInstall              = "install"
	lifecycleUpgrade              = "upgrade"
)

func NewLifecycle() *tool.Lifecycle {
	return &tool.Lifecycle{}
}

func WithLifecycle(ctx context.Context, lifecycle *tool.Lifecycle) context.Context {
	if lifecycle == nil {
		return ctx
	}
	return context.WithValue(ctx, tool.LifecycleKey{}, lifecycle)
}

func HostInitOnce(opts *tool.CommandOptions, name string, fn func() error) error {
	if opts == nil {
		return fn()
	}
	return opts.RunOnce(lifecycleHostInit, name, fn)
}

func SandboxContextSetupOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleSandboxContextSetup, name, fn)
}

func RegisterContextFilesOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleRegisterContextFiles, name, fn)
}

func SandboxInitOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleSandboxInit, name, fn)
}

func InstallOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleInstall, name, fn)
}

func UpgradeOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleUpgrade, name, fn)
}

func runOnce(ctx context.Context, phase, name string, fn func() error) error {
	if ctx == nil {
		return fn()
	}
	lifecycle, _ := ctx.Value(tool.LifecycleKey{}).(*tool.Lifecycle)
	if lifecycle == nil {
		return fn()
	}
	return lifecycle.RunOnce(phase, name, fn)
}
