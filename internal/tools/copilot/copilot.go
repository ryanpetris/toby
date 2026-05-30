package copilot

import (
	"context"
	"fmt"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/copilotconfig"
	"petris.dev/toby/internal/httpproxy"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.copilot", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths  config.Paths
	NPM    tool.Tool           `name:"npm"`
	Config *tobyconfig.Service `optional:"true"`
	Proxy  *httpproxy.Service  `optional:"true"`
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
			toolutil.Base(tool.CopilotToolName, "Launch Copilot", tool.GroupSystem, tool.GroupVCS),
			[]string{".copilot"},
			[]string{".copilot"},
			[]string{"npm", "install", "-g", "@github/copilot"},
			nil,
		),
		npm:    params.NPM,
		config: params.Config,
		proxy:  params.Proxy,
	}
	return Result{Service: svc, Registry: svc}
}

type copilotTool struct {
	*tool.Simple
	npm    tool.Tool
	config *tobyconfig.Service
	proxy  *httpproxy.Service
}

func (t *copilotTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *copilotTool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), t.Simple.Binds())
}

func (t *copilotTool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), t.Simple.PathEntries())
}

func (t *copilotTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *copilotTool) SandboxContextSetup(ctx *tool.RunContext) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	if err := t.Simple.SandboxContextSetup(ctx); err != nil {
		return err
	}
	return tool.SandboxContextSetupOnce(ctx, t.Name()+".context", func() error {
		contextDir := copilotconfig.InstructionsDir(ctx.Sandbox.TobyContextDir())
		if existing := strings.TrimSpace(ctx.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"]); existing != "" {
			ctx.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"] = contextDir + "," + existing
		} else {
			ctx.Env["COPILOT_CUSTOM_INSTRUCTIONS_DIRS"] = contextDir
		}
		return nil
	})
}

func (t *copilotTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.SandboxInitDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx, run)
}

func (t *copilotTool) RegisterContextFiles(ctx context.Context, run *tool.RunContext) error {
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	if registrar, ok := t.npm.(tool.ContextFileTool); ok {
		if err := registrar.RegisterContextFiles(ctx, run); err != nil {
			return err
		}
	}
	return copilotconfig.RegisterContextFiles(run.ContextFiles, run.ContextFiles.InstructionContents(), t.config, run.Env[control.EnvControlHost], run.TobyMCPURL, t.proxy)
}

func (t *copilotTool) Install(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.InstallDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx, run)
}

func (t *copilotTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.UpgradeDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx, run)
}

func (t *copilotTool) Launch(ctx context.Context, run *tool.RunContext) error {
	argv := append([]string{"copilot"}, contextFlags(run.Sandbox.TobyContextDir())...)
	argv = append(argv, run.Extra...)
	return tool.RunCommand(ctx, run.Launch, argv, tool.ExecOptions{})
}

func contextFlags(contextDir string) []string {
	return []string{"--additional-mcp-config", "@" + copilotconfig.MCPConfigPath(contextDir)}
}
