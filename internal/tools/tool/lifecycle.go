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

type LifecycleKey struct{}

type Lifecycle struct {
	done map[string]bool
}

func newLifecycle() *Lifecycle {
	return &Lifecycle{}
}

func (l *Lifecycle) RunOnce(phase, name string, fn func() error) error {
	if l == nil {
		return fn()
	}
	if l.done == nil {
		l.done = map[string]bool{}
	}
	return once(l.done, phase, name, fn)
}

func (o *CommandOptions) RunOnce(phase, name string, fn func() error) error {
	if o == nil {
		return fn()
	}
	if o.lifecycle == nil {
		o.lifecycle = map[string]bool{}
	}
	return once(o.lifecycle, phase, name, fn)
}

func hostInitOnce(opts *CommandOptions, name string, fn func() error) error {
	return commandOnce(opts, lifecycleHostInit, name, fn)
}

func sandboxContextSetupOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleSandboxContextSetup, name, fn)
}

func registerContextFilesOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleRegisterContextFiles, name, fn)
}

func sandboxInitOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleSandboxInit, name, fn)
}

func installOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleInstall, name, fn)
}

func upgradeOnce(ctx context.Context, name string, fn func() error) error {
	return runOnce(ctx, lifecycleUpgrade, name, fn)
}

func commandOnce(opts *CommandOptions, phase, name string, fn func() error) error {
	return opts.RunOnce(phase, name, fn)
}

func runOnce(ctx context.Context, phase, name string, fn func() error) error {
	if ctx == nil {
		return fn()
	}
	lifecycle, _ := ctx.Value(LifecycleKey{}).(*Lifecycle)
	if lifecycle == nil {
		return fn()
	}
	return lifecycle.RunOnce(phase, name, fn)
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
