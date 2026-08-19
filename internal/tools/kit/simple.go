package kit

// Simple is a reusable Tool implementation for tools backed by one persistent
// tool volume and an optional fixed install command.

import (
	"context"
	pathpkg "path"

	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/helpers"
)

// Simple is a config-driven tool: it mounts a state subpath, optionally installs
// a command, seeds environment, and launches a command. Tools that need only
// this behavior embed *Simple; others embed it and override individual phases.
type Simple struct {
	tools.Base
	Sandbox             sandbox.Service
	SandboxSubpath      []string
	ExtraMounts         []mount.Request
	InstallCommand      []string
	InstallCheckCommand string
	SandboxEnv          map[string]string
	PathAppend          string
	LaunchCommand       string
	LaunchArgs          []string
	YoloArgs            []string
	Yolo                func() bool
}

// PrepareHost prepares host-side state required by the tool.
func (t *Simple) PrepareHost(_ context.Context, _ *tools.Options) error {
	if req, ok := t.mountRequest(); ok {
		if err := t.Sandbox.AddMount(req); err != nil {
			return err
		}
	}
	for _, req := range t.ExtraMounts {
		if err := t.Sandbox.AddMount(req); err != nil {
			return err
		}
	}
	return nil
}

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

// ConfigureSandbox adds the tool's sandbox configuration.
func (t *Simple) ConfigureSandbox(ctx context.Context) error {
	for key, value := range t.SandboxEnv {
		if err := t.Sandbox.SetEnvironment(ctx, key, value); err != nil {
			return err
		}
	}
	if t.PathAppend == "" {
		return nil
	}
	return t.Sandbox.AppendEnvironment(ctx, "PATH", t.PathAppend, ":")
}

// Install installs the tool when needed.
func (t *Simple) Install(ctx context.Context, force bool) error {
	if len(t.InstallCommand) == 0 {
		return nil
	}
	check := t.InstallCheckCommand
	if check == "" {
		check = t.Name()
	}
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.Sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			check,
		)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}
	}
	_, err := t.Sandbox.Exec(
		ctx,
		t.InstallCommand,
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

// Launch runs the tool's foreground application.
func (t *Simple) Launch(ctx context.Context, extra []string) error {
	command := t.LaunchCommand
	if command == "" {
		command = t.Name()
	}
	argv := append([]string{command}, t.LaunchArgs...)
	if t.YoloEnabled() {
		argv = append(argv, t.YoloArgs...)
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
	return err
}

// YoloEnabled reports whether this launch should add YoloArgs.
func (t *Simple) YoloEnabled() bool {
	return t != nil && t.Yolo != nil && t.Yolo()
}
