package docker

import (
	"context"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tools/helpers"
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

func (t *dockerTool) UsesToolState() bool { return true }

func (t *dockerTool) HostInit(_ context.Context, opts *tool.CommandOptions) error {
	return helpers.HostInitOnce(opts, t.Name(), func() error {
		if opts.ToolStateFor(t.Name()) == tool.ToolStateHost {
			bind := tool.Bind{HostPath: filepath.Join(t.paths.Home, ".docker"), Target: helpers.HomeTarget(".docker"), Type: tool.BindReadOnly, Optional: true, State: true}
			bind.HostPath = helpers.ResolveStateBindHostPath(opts.ToolStateRootFor(t.Name()), bind)
			if err := t.sandbox.AddBind(bind); err != nil {
				return err
			}
		}
		return t.sandbox.AddBind(tool.Bind{HostPath: "/var/run/docker.sock", Target: helpers.AbsoluteTarget("/var/run/docker.sock"), Type: tool.BindDev, Optional: true})
	})
}

func (t *dockerTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"docker"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}
