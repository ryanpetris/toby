package copilot

import (
	"context"
	"petris.dev/toby/container/layout"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	copilotconfig "petris.dev/toby/internal/dirty/tools/copilot/config"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.copilot", fx.Provide(Provide))

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
	svc := &copilotTool{
		Simple: toolutil.NewSimple(
			params.Paths,
			params.Sandbox,
			toolutil.DependentBase(tools.CopilotToolName, "Launch Copilot", 100, []string{tools.NpmToolName}, tools.GroupAI, tools.GroupSystem, tools.GroupVCS),
			[]string{".copilot"},
			[]string{".copilot"},
			[]string{"npm", "install", "-g", "@github/copilot"},
			nil,
		),
		config:       params.Config,
		proxy:        params.Proxy,
		mcpProxy:     params.MCPProxy,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type copilotTool struct {
	*toolutil.Simple
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	contextFiles *contextfiles.Service
	yolo         bool
}

func (t *copilotTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	if opts != nil {
		t.yolo = opts.YoloEnabled()
	}
	return t.Simple.PrepareHost(ctx, opts)
}

func (t *copilotTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}
	return helpers.SandboxContextSetupOnce(ctx, t.Name()+".context", func() error {
		contextDir := copilotconfig.InstructionsDir(layout.Context)
		return t.Sandbox.PrependEnvironment(ctx, "COPILOT_CUSTOM_INSTRUCTIONS_DIRS", contextDir, ",")
	})
}

func (t *copilotTool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *copilotTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
		return copilotconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy, t.mcpProxy)
	})
}

func (t *copilotTool) Launch(ctx context.Context, extra []string) error {
	argv := append([]string{"copilot"}, contextFlags(layout.Context)...)
	if t.yolo {
		argv = append(argv, "--allow-all-tools")
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
	return err
}

func contextFlags(contextDir string) []string {
	return []string{"--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(contextDir)}
}
