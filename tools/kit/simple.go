package kit

// Simple: a reusable Tool implementation for tools installed by a fixed install
// command, seeding environment and launching a command. Tool state persists in the
// shared home volume, so no per-tool state mount is declared.

import (
	"context"

	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
)

// Simple is a config-driven tool: it optionally installs a command, seeds
// environment, and launches a command. Tools that need only this behavior embed
// *Simple; others embed it and override individual phases.
type Simple struct {
	tools.Base
	Sandbox             sandbox.Service
	InstallCommand      []string
	InstallCheckCommand string
	SandboxEnv          map[string]string
	Command             string
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

func (t *Simple) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	command := t.Command
	if command == "" {
		command = t.Name()
	}
	return append([]string{command}, extra...), nil
}
