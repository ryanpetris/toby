package codex

import (
	"context"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	codexconfig "petris.dev/toby/internal/tools/codex/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.codex", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths        config.Paths
	NPM          tool.Tool           `name:"npm"`
	Config       *tobyconfig.Service `optional:"true"`
	Proxy        *httpproxy.Service  `optional:"true"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"codex"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &codexTool{
		Simple: toolutil.Simple(
			params.Paths,
			params.Sandbox,
			toolutil.Base(tool.CodexToolName, "Launch Codex", tool.GroupSystem, tool.GroupVCS),
			[]string{".codex"},
			[]string{".codex"},
			[]string{"npm", "install", "-g", "@openai/codex"},
			nil,
		),
		npm:          params.NPM,
		config:       params.Config,
		proxy:        params.Proxy,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type codexTool struct {
	*tool.Simple
	npm          tool.Tool
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	contextFiles *contextfiles.Service
}

func (t *codexTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *codexTool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), t.Simple.Binds())
}

func (t *codexTool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), t.Simple.PathEntries())
}

func (t *codexTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *codexTool) SandboxContextSetup(ctx context.Context) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxContextSetup(ctx)
}

func (t *codexTool) SandboxInit(ctx context.Context) error {
	if err := toolutil.SandboxInitDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx)
}

func (t *codexTool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return tool.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		if registrar, ok := t.npm.(tool.ContextFileTool); ok {
			return registrar.RegisterContextFiles(ctx, opts)
		}
		return nil
	})
}

func (t *codexTool) Install(ctx context.Context) error {
	if err := toolutil.InstallDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx)
}

func (t *codexTool) Upgrade(ctx context.Context) error {
	if err := toolutil.UpgradeDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx)
}

func (t *codexTool) Launch(ctx context.Context, extra []string) error {
	args, err := t.launchArgs(extra)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(ctx, append([]string{"codex"}, args...), tool.ExecOptions{Foreground: true})
	return err
}

func (t *codexTool) launchArgs(extra []string) ([]string, error) {
	controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
	args, err := codexconfig.ConfigArgs(t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy)
	if err != nil {
		return nil, err
	}
	args = append(args, extra...)
	return args, nil
}
