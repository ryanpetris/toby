package copilot

import (
	"context"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
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
	NPM          tool.Tool           `name:"npm"`
	Config       *tobyconfig.Service `optional:"true"`
	Proxy        *httpproxy.Service  `optional:"true"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"copilot"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &copilotTool{
		Simple: toolutil.Simple(
			params.Paths,
			params.Sandbox,
			toolutil.Base(tool.CopilotToolName, "Launch Copilot", tool.GroupSystem, tool.GroupVCS),
			[]string{".copilot"},
			[]string{".copilot"},
			[]string{"npm", "install", "-g", "@github/copilot"},
			nil,
		),
		npm:          params.NPM,
		config:       params.Config,
		proxy:        params.Proxy,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type copilotTool struct {
	*tool.Simple
	npm          tool.Tool
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	contextFiles *contextfiles.Service
}

func (t *copilotTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *copilotTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *copilotTool) SandboxContextSetup(ctx context.Context) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	if err := t.Simple.SandboxContextSetup(ctx); err != nil {
		return err
	}
	return helpers.SandboxContextSetupOnce(ctx, t.Name()+".context", func() error {
		contextDir := copilotconfig.InstructionsDir(t.Sandbox.Paths().Context)
		return t.Sandbox.PrependEnvironment(ctx, "COPILOT_CUSTOM_INSTRUCTIONS_DIRS", contextDir, ",")
	})
}

func (t *copilotTool) SandboxInit(ctx context.Context) error {
	if err := toolutil.SandboxInitDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx)
}

func (t *copilotTool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		if registrar, ok := t.npm.(tool.ContextFileTool); ok {
			if err := registrar.RegisterContextFiles(ctx, opts); err != nil {
				return err
			}
		}
		controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
		return copilotconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy)
	})
}

func (t *copilotTool) Install(ctx context.Context) error {
	if err := toolutil.InstallDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx)
}

func (t *copilotTool) Upgrade(ctx context.Context) error {
	if err := toolutil.UpgradeDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx)
}

func (t *copilotTool) Launch(ctx context.Context, extra []string) error {
	argv := append([]string{"copilot"}, contextFlags(t.Sandbox.Paths().Context)...)
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, tool.ExecOptions{Foreground: true})
	return err
}

func contextFlags(contextDir string) []string {
	return []string{"--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(contextDir)}
}
