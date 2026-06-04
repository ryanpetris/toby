package tool

import (
	"context"
	"errors"
	pathpkg "path"

	"petris.dev/toby/container/mount"
)

type Simple struct {
	Base
	Sandbox             SandboxService
	RootDir             string
	HostSubpath         []string
	SandboxSubpath      []string
	Access              mount.Access
	InstallCommand      []string
	InstallCheckCommand string
	SandboxEnv          map[string]string
	LaunchCommand       string
}

func (t *Simple) HostInit(_ context.Context, opts *CommandOptions) error {
	return hostInitOnce(opts, t.Name(), func() error {
		req, ok := t.mountRequest()
		if !ok {
			return nil
		}
		_, err := t.Sandbox.AddMount(req)
		return err
	})
}

func (t *Simple) UsesManagedMounts() bool { return len(t.HostSubpath) > 0 }

func (t *Simple) mountRequest() (mount.Request, bool) {
	if len(t.HostSubpath) == 0 {
		return mount.Request{}, false
	}
	sandboxParts := t.SandboxSubpath
	if len(sandboxParts) == 0 {
		sandboxParts = t.HostSubpath
	}
	access := t.Access
	if access == "" {
		access = mount.AccessRegular
	}
	return mount.Request{
		Key:    mount.Key{Type: mount.TypeTool, Name: t.Name(), Purpose: "state"},
		Target: "~/" + pathpkg.Join(sandboxParts...),
		Access: access,
	}, true
}

func (t *Simple) SandboxContextSetup(ctx context.Context) error {
	return sandboxContextSetupOnce(ctx, t.Name(), func() error {
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
	once := installOnce
	if force {
		once = upgradeOnce
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
		exists, err := commandExists(ctx, t.Sandbox.Exec, ExecOptions{HideOutput: true}, check)
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

func commandExists(ctx context.Context, exec func(context.Context, []string, ExecOptions) (int, error), opts ExecOptions, command string) (bool, error) {
	rc, err := exec(ctx, []string{"which", command}, opts)
	if err != nil {
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) && err.Error() == "" {
			return false, nil
		}
		return false, err
	}
	return rc == 0, nil
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
