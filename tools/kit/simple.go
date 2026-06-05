package kit

// Simple: a reusable Tool implementation for tools installed by copying a host
// subpath into the sandbox and running a fixed install command.

import (
	"context"
	pathpkg "path"

	"petris.dev/toby/container/mount"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
)

// Simple is a config-driven tool: it mounts a state subpath, optionally installs
// a command, seeds environment, and launches a command. Tools that need only
// this behavior embed *Simple; others embed it and override individual phases.
type Simple struct {
	tools.Base
	Sandbox             sandbox.Service
	SandboxSubpath      []string
	InstallCommand      []string
	InstallCheckCommand string
	SandboxEnv          map[string]string
	LaunchCommand       string
}

func (t *Simple) PrepareHost(_ context.Context, _ *tools.Options) error {
	req, ok := t.mountRequest()
	if !ok {
		return nil
	}
	_, err := t.Sandbox.AddMount(req)
	return err
}

func (t *Simple) UsesManagedMounts() bool { return len(t.SandboxSubpath) > 0 }

func (t *Simple) mountRequest() (mount.Request, bool) {
	if len(t.SandboxSubpath) == 0 {
		return mount.Request{}, false
	}
	return mount.Request{
		Key:    mount.Key{Type: mount.TypeTool, Name: t.Name(), Purpose: "state"},
		Target: "~/" + pathpkg.Join(t.SandboxSubpath...),
		Access: mount.AccessRegular,
	}, true
}

func (t *Simple) ConfigureSandbox(ctx context.Context) error {
	for key, value := range t.SandboxEnv {
		if err := t.Sandbox.SetEnvironment(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}

func (t *Simple) Install(ctx context.Context, force bool) error {
	if len(t.InstallCommand) == 0 {
		return nil
	}
	check := t.InstallCheckCommand
	if check == "" {
		check = t.Name()
	}
	if !force {
		exists, err := helpers.CommandExists(ctx, t.Sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, check)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	_, err := t.Sandbox.Exec(ctx, t.InstallCommand, sandbox.ExecOptions{})
	return err
}

func (t *Simple) Launch(ctx context.Context, extra []string) error {
	command := t.LaunchCommand
	if command == "" {
		command = t.Name()
	}
	argv := append([]string{command}, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
	return err
}
