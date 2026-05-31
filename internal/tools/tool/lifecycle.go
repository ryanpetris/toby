package tool

const (
	lifecycleHostInit            = "host-init"
	lifecycleSandboxContextSetup = "sandbox-context-setup"
	lifecycleSandboxInit         = "sandbox-init"
	lifecycleInstall             = "install"
	lifecycleUpgrade             = "upgrade"
)

func HostInitOnce(opts *CommandOptions, name string, fn func() error) error {
	return commandOnce(opts, lifecycleHostInit, name, fn)
}

func SandboxContextSetupOnce(ctx *RunContext, name string, fn func() error) error {
	return runOnce(ctx, lifecycleSandboxContextSetup, name, fn)
}

func SandboxInitOnce(ctx *RunContext, name string, fn func() error) error {
	return runOnce(ctx, lifecycleSandboxInit, name, fn)
}

func InstallOnce(ctx *RunContext, name string, fn func() error) error {
	return runOnce(ctx, lifecycleInstall, name, fn)
}

func UpgradeOnce(ctx *RunContext, name string, fn func() error) error {
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

func runOnce(ctx *RunContext, phase, name string, fn func() error) error {
	if ctx == nil {
		return fn()
	}
	if ctx.Options != nil {
		return commandOnce(ctx.Options, phase, name, fn)
	}
	if ctx.lifecycle == nil {
		ctx.lifecycle = map[string]bool{}
	}
	return once(ctx.lifecycle, phase, name, fn)
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
