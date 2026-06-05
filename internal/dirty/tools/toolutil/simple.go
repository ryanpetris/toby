package toolutil

import (
	"context"
	pathpkg "path"

	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/dirty/tools/helpers"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
)

// Simple is a config-driven tool: it mounts a state subpath, optionally installs
// a command, seeds environment, and launches a command. Tools that need only
// this behavior embed *Simple; others embed it and override individual phases.
type Simple struct {
	tools.Base
	Sandbox             sandbox.Service
	RootDir             string
	HostSubpath         []string
	SandboxSubpath      []string
	Access              mount.Access
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
