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
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.docker", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(paths config.Paths, sandbox sandbox.Service) Result {
	svc := &dockerTool{
		Base:    kit.Base(tools.DockerToolName, "Launch Docker", tools.GroupSystem, tools.GroupVCS),
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

func (t *dockerTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"docker"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
