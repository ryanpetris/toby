// Package docker provides the Docker CLI tool: it binds the host Docker socket
// and ~/.docker into the sandbox so containers can be managed from inside it.
package docker

import (
	"context"
	"path/filepath"

	"petris.dev/toby/config"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.docker", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "docker"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Docker",
	Group:         tools.GroupSystem,
	ContextGroups: []string{tools.GroupSystem, tools.GroupVCS},
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(paths config.Paths, sandbox sandbox.Service) Result {
	svc := &dockerTool{
		Base:    tools.Base{Metadata: Meta},
		paths:   paths,
		sandbox: sandbox,
	}
	return Result{Service: svc}
}

type dockerTool struct {
	tools.Base
	paths   config.Paths
	sandbox sandbox.Service
}

var _ tools.Tool = (*dockerTool)(nil)

func (t *dockerTool) PrepareHost(_ context.Context, opts *tools.Options) error {
	if err := t.sandbox.AddBind(mount.Bind{HostPath: filepath.Join(t.paths.Home, ".docker"), Target: "~/.docker", Access: mount.AccessReadOnly, Optional: true}); err != nil {
		return err
	}
	return t.sandbox.AddBind(mount.Bind{HostPath: layout.DockerSocket, Target: layout.DockerSocket, Access: mount.AccessDev, Optional: true})
}

func (t *dockerTool) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	return append([]string{"docker"}, extra...), nil
}
