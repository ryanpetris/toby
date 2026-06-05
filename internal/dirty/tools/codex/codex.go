package codex

import (
	"context"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	codexconfig "petris.dev/toby/internal/dirty/tools/codex/config"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.codex", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths        config.Paths
	Config       *tobyconfig.Service `optional:"true"`
	Proxy        *httpproxy.Service  `optional:"true"`
	MCPProxy     *mcpproxy.Service   `optional:"true"`
	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &codexTool{
		Simple: toolutil.NewSimple(
			params.Paths,
			params.Sandbox,
			toolutil.DependentBase(tools.CodexToolName, "Launch Codex", 100, []string{tools.NpmToolName}, tools.GroupAI, tools.GroupSystem, tools.GroupVCS),
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
	*toolutil.Simple
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	contextFiles *contextfiles.Service
	yolo         bool
}

func (t *codexTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	if opts != nil {
		t.yolo = opts.YoloEnabled()
	}
	return t.Simple.PrepareHost(ctx, opts)
}

func (t *codexTool) ConfigureSandbox(ctx context.Context) error {
	return t.Simple.ConfigureSandbox(ctx)
}

func (t *codexTool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *codexTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error { return nil })
}

func (t *codexTool) Launch(ctx context.Context, extra []string) error {
	args, err := t.launchArgs(extra)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(ctx, append([]string{"codex"}, args...), sandbox.ExecOptions{Foreground: true})
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
