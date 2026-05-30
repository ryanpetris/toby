package docker

import (
	"context"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.docker", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service  tool.Tool `name:"docker"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(paths config.Paths) Result {
	svc := &dockerTool{
		Base:  toolutil.Base(tool.DockerToolName, "Launch Docker", tool.GroupSystem, tool.GroupVCS),
		paths: paths,
	}
	return Result{Service: svc, Registry: svc}
}

type dockerTool struct {
	tool.Base
	paths config.Paths
}

func (t *dockerTool) Binds() []tool.Bind {
	return []tool.Bind{
		{HostPath: filepath.Join(t.paths.Home, ".docker"), Target: tool.HomeTarget(".docker"), Type: tool.BindReadOnly, Optional: true},
		{HostPath: "/var/run/docker.sock", Target: tool.AbsoluteTarget("/var/run/docker.sock"), Type: tool.BindDev, Optional: true},
	}
}

func (t *dockerTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"docker"}, run.Extra...), tool.ExecOptions{})
}
