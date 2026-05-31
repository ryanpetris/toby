package tool

import "context"

const (
	lifecycleHostInit             = "host-init"
	lifecycleSandboxContextSetup  = "sandbox-context-setup"
	lifecycleRegisterContextFiles = "register-context-files"
	lifecycleSandboxInit          = "sandbox-init"
	lifecycleInstall              = "install"
	lifecycleUpgrade              = "upgrade"
)

type lifecycleKey struct{}

type Lifecycle struct {
	done map[string]bool
}

func NewLifecycle() *Lifecycle {
	return &Lifecycle{done: map[string]bool{}}
}

func WithLifecycle(ctx context.Context, lifecycle *Lifecycle) context.Context {
	if lifecycle == nil {
		return ctx
	}
	return context.WithValue(ctx, lifecycleKey{}, lifecycle)
}

func HostInitOnce(opts *CommandOptions, name string, fn func() error) error {
	return commandOnce(opts, lifecycleHostInit, name, fn)
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

func commandOnce(opts *CommandOptions, phase, name string, fn func() error) error {
	if opts == nil {
		return fn()
	}
	if opts.lifecycle == nil {
		opts.lifecycle = map[string]bool{}
	}
	return once(opts.lifecycle, phase, name, fn)
}

func runOnce(ctx context.Context, phase, name string, fn func() error) error {
	if ctx == nil {
		return fn()
	}
	lifecycle, _ := ctx.Value(lifecycleKey{}).(*Lifecycle)
	if lifecycle == nil {
		return fn()
	}
	if lifecycle.done == nil {
		lifecycle.done = map[string]bool{}
	}
	return once(lifecycle.done, phase, name, fn)
}

func once(done map[string]bool, phase, name string, fn func() error) error {
	key := phase + "\x00" + name
	if done[key] {
		return nil
	}
	if err := fn(); err != nil {
		return err
	}
	done[key] = true
	return nil
}
