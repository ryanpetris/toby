package docker

import (
	"context"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.docker", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service  tool.Tool `name:"docker"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(paths config.Paths, sandbox tool.SandboxService) Result {
	svc := &dockerTool{
		Base:    toolutil.Base(tool.DockerToolName, "Launch Docker", tool.GroupSystem, tool.GroupVCS),
		paths:   paths,
		sandbox: sandbox,
	}
	return Result{Service: svc, Registry: svc}
}

type dockerTool struct {
	tool.Base
	paths   config.Paths
	sandbox tool.SandboxService
}

func (t *dockerTool) Binds() []tool.Bind {
	return []tool.Bind{
		{HostPath: filepath.Join(t.paths.Home, ".docker"), Target: tool.HomeTarget(".docker"), Type: tool.BindReadOnly, Optional: true, State: true},
		{HostPath: "/var/run/docker.sock", Target: tool.AbsoluteTarget("/var/run/docker.sock"), Type: tool.BindDev, Optional: true},
	}
}

func (t *dockerTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"docker"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}
