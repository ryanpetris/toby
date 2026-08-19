// Package docker provides the Docker CLI tool through an explicit host-socket
// relay and read-only Docker client configuration.
package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/socketrelay"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/helpers"
)

// Name is this tool's canonical identifier.
const Name = "docker"

const hostSocketPath = "/var/run/docker.sock"
const sandboxSocketPath = layout.Runtime + "/docker.sock"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Docker",
	LaunchHelp:    "Launch Docker",
	Group:         tools.GroupSystem,
	ContextGroups: []string{tools.GroupSystem, tools.GroupVCS},
}

// provide constructs the tool implementation.
func provide(
	paths config.Paths,
	sandbox sandbox.Service,
	warnings *warning.Service,
) result {
	svc := &dockerTool{
		Base:     tools.Base{Metadata: Meta},
		paths:    paths,
		sandbox:  sandbox,
		warnings: warnings,
		socket:   hostSocketPath,
	}
	return result{Service: svc}
}

type dockerTool struct {
	tools.Base
	paths    config.Paths
	sandbox  sandbox.Service
	warnings *warning.Service
	socket   string
	disabled bool
}

var _ tools.Tool = (*dockerTool)(nil)
var _ socketrelay.Contributor = (*dockerTool)(nil)

func (t *dockerTool) PrepareHost(_ context.Context, opts *tools.Options) error {
	info, err := os.Stat(t.socket)
	if err != nil {
		if opts != nil && opts.Primary != Name {
			t.disabled = true
			if t.warnings != nil {
				t.warnings.Warn(
					warning.DockerSocketMissing,
					fmt.Sprintf(
						"Docker socket %s is unavailable; skipping the Docker tool",
						t.socket,
					),
					"path", t.socket,
				)
			}
			return nil
		}
		return fmt.Errorf(
			"docker tool requires host socket %s: %w",
			t.socket,
			err,
		)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf(
			"docker tool requires host socket %s, but it is not a Unix socket",
			t.socket,
		)
	}

	if err := t.sandbox.AddBind(mount.Bind{
		HostPath: filepath.Join(t.paths.Home, ".docker"),
		Target:   filepath.Join(layout.Home, ".docker"),
		Access:   mount.AccessReadOnly,
		Optional: true,
	}); err != nil {
		return err
	}

	return nil
}

func (t *dockerTool) ConfigureSandbox(ctx context.Context) error {
	if t.disabled {
		return nil
	}
	if err := t.sandbox.SetEnvironment(
		ctx,
		"DOCKER_CONTEXT",
		"",
	); err != nil {
		return err
	}

	return t.sandbox.SetEnvironment(
		ctx,
		"DOCKER_HOST",
		"unix://"+sandboxSocketPath,
	)
}

func (t *dockerTool) SocketRelays() ([]socketrelay.Request, error) {
	if t.disabled {
		return nil, nil
	}
	return []socketrelay.Request{{
		HostSocket:    t.socket,
		SandboxSocket: sandboxSocketPath,
	}}, nil
}

func (t *dockerTool) Install(ctx context.Context, _ bool) error {
	if t.disabled {
		return nil
	}
	exists, err := helpers.CommandExists(
		ctx,
		t.sandbox.Exec,
		sandbox.ExecOptions{
			HideOutput: true,
			Status:     "Checking availability",
		},
		"docker",
	)
	if err != nil {
		return fmt.Errorf("check Docker CLI availability: %w", err)
	}
	if !exists {
		return fmt.Errorf(
			"docker tool requires the Docker CLI in the sandbox image",
		)
	}
	return nil
}

func (t *dockerTool) Launch(ctx context.Context, extra []string) error {
	if t.disabled {
		return fmt.Errorf("docker tool is unavailable")
	}
	_, err := t.sandbox.Exec(ctx, append([]string{"docker"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
