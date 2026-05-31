package tool

import (
	"context"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/tools/helpers"
)

type Simple struct {
	Base
	Sandbox             SandboxService
	RootDir             string
	HostSubpath         []string
	SandboxSubpath      []string
	BindType            BindType
	InstallCommand      []string
	InstallCheckCommand string
	SandboxEnv          map[string]string
	LaunchCommand       string
}

func (t *Simple) HostInit(_ context.Context, opts *CommandOptions) error {
	if opts.ToolStateFor(t.Name()) != ToolStateHost {
		return nil
	}
	return HostInitOnce(opts, t.Name(), func() error {
		if len(t.HostSubpath) == 0 {
			return nil
		}
		root := opts.ToolStateRootFor(t.Name())
		return os.MkdirAll(filepath.Join(append([]string{root}, t.HostSubpath...)...), 0o755)
	})
}

func (t *Simple) Binds() []Bind {
	if len(t.HostSubpath) == 0 {
		return nil
	}
	sandboxParts := t.SandboxSubpath
	if len(sandboxParts) == 0 {
		sandboxParts = t.HostSubpath
	}
	bindType := t.BindType
	if bindType == "" {
		bindType = BindRegular
	}
	return []Bind{{
		HostPath: filepath.Join(append([]string{t.RootDir}, t.HostSubpath...)...),
		Target:   HomeTarget(sandboxParts...),
		Type:     bindType,
		State:    true,
	}}
}

func (t *Simple) SandboxContextSetup(ctx context.Context) error {
	return SandboxContextSetupOnce(ctx, t.Name(), func() error {
		for key, value := range t.SandboxEnv {
			if err := t.Sandbox.SetEnvironment(ctx, key, value); err != nil {
				return err
			}
		}
		return nil
	})
}

func (t *Simple) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *Simple) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *Simple) install(ctx context.Context, force bool) error {
	once := InstallOnce
	if force {
		once = UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		return t.runInstall(ctx, force)
	})
}

func (t *Simple) runInstall(ctx context.Context, force bool) error {
	if len(t.InstallCommand) == 0 {
		return nil
	}
	check := t.InstallCheckCommand
	if check == "" {
		check = t.Name()
	}
	if !force {
		exists, err := helpers.CommandExists(ctx, t.Sandbox.Exec, ExecOptions{HideOutput: true}, check)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	_, err := t.Sandbox.Exec(ctx, t.InstallCommand, ExecOptions{})
	return err
}

func (t *Simple) Launch(ctx context.Context, extra []string) error {
	command := t.LaunchCommand
	if command == "" {
		command = t.Name()
	}
	argv := append([]string{command}, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, ExecOptions{Foreground: true})
	return err
}
