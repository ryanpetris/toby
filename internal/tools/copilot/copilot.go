package copilot

import (
	"context"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
	copilotconfig "petris.dev/toby/internal/tools/copilot/config"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.copilot", fx.Provide(Provide))

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
	svc := &copilotTool{
		Simple: toolutil.Simple(
			params.Paths,
			params.Sandbox,
			toolutil.DependentBase(tool.CopilotToolName, "Launch Copilot", 100, []string{tool.NpmToolName}, tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
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
	*tool.Simple
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	contextFiles *contextfiles.Service
	yolo         bool
}

func (t *copilotTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if opts != nil {
		t.yolo = opts.YoloEnabled()
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *copilotTool) SandboxContextSetup(ctx context.Context) error {
	if err := t.Simple.SandboxContextSetup(ctx); err != nil {
		return err
	}
	return helpers.SandboxContextSetupOnce(ctx, t.Name()+".context", func() error {
		contextDir := copilotconfig.InstructionsDir(t.Sandbox.Paths().Context)
		return t.Sandbox.PrependEnvironment(ctx, "COPILOT_CUSTOM_INSTRUCTIONS_DIRS", contextDir, ",")
	})
}

func (t *copilotTool) SandboxInit(ctx context.Context) error {
	return t.Simple.SandboxInit(ctx)
}

func (t *copilotTool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
		return copilotconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy, t.mcpProxy)
	})
}

func (t *copilotTool) Install(ctx context.Context) error {
	return t.Simple.Install(ctx)
}

func (t *copilotTool) Upgrade(ctx context.Context) error {
	return t.Simple.Upgrade(ctx)
}

func (t *copilotTool) Launch(ctx context.Context, extra []string) error {
	argv := append([]string{"copilot"}, contextFlags(t.Sandbox.Paths().Context)...)
	if t.yolo {
		argv = append(argv, "--allow-all-tools")
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, tool.ExecOptions{Foreground: true})
	return err
}

func contextFlags(contextDir string) []string {
	return []string{"--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(contextDir)}
}
