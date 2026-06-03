package codex

import (
	"context"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
	codexconfig "petris.dev/toby/internal/tools/codex/config"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.codex", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths        config.Paths
	Config       *tobyconfig.Service `optional:"true"`
	Proxy        *httpproxy.Service  `optional:"true"`
	MCPProxy     *mcpproxy.Service   `optional:"true"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &codexTool{
		Simple: toolutil.Simple(
			params.Paths,
			params.Sandbox,
			toolutil.DependentBase(tool.CodexToolName, "Launch Codex", 100, []string{tool.NpmToolName}, tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
			[]string{".codex"},
			[]string{".codex"},
			[]string{"npm", "install", "-g", "@openai/codex"},
			nil,
		),
		config:       params.Config,
		proxy:        params.Proxy,
		mcpProxy:     params.MCPProxy,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type codexTool struct {
	*tool.Simple
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	contextFiles *contextfiles.Service
	yolo         bool
}

func (t *codexTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if opts != nil {
		t.yolo = opts.YoloEnabled()
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *codexTool) SandboxContextSetup(ctx context.Context) error {
	return t.Simple.SandboxContextSetup(ctx)
}

func (t *codexTool) SandboxInit(ctx context.Context) error {
	return t.Simple.SandboxInit(ctx)
}

func (t *codexTool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error { return nil })
}

func (t *codexTool) Install(ctx context.Context) error {
	return t.Simple.Install(ctx)
}

func (t *codexTool) Upgrade(ctx context.Context) error {
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
	args, err := codexconfig.ConfigArgs(t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy, t.mcpProxy)
	if err != nil {
		return nil, err
	}
	if t.yolo {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, extra...)
	return args, nil
}
