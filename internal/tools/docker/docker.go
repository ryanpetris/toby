package docker

import (
	"context"
	"path/filepath"

	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.docker", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service tool.Tool `group:"toby.tools"`
}

func Provide(paths config.Paths, sandbox tool.SandboxService) Result {
	svc := &dockerTool{
		Base:    toolutil.Base(tool.DockerToolName, "Launch Docker", tool.GroupSystem, tool.GroupVCS),
		paths:   paths,
		sandbox: sandbox,
	}
	return Result{Service: svc}
}

type dockerTool struct {
	tool.Base
	paths   config.Paths
	sandbox tool.SandboxService
}

func (t *dockerTool) HostInit(_ context.Context, opts *tool.CommandOptions) error {
	return helpers.HostInitOnce(opts, t.Name(), func() error {
		if err := t.sandbox.AddBind(mount.Bind{HostPath: filepath.Join(t.paths.Home, ".docker"), Target: "~/.docker", Access: mount.AccessReadOnly, Optional: true}); err != nil {
			return err
		}
		return t.sandbox.AddBind(mount.Bind{HostPath: layout.DockerSocket, Target: layout.DockerSocket, Access: mount.AccessDev, Optional: true})
	})
}

func (t *dockerTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"docker"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}
