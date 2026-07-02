// Package copilot provides the GitHub Copilot CLI agent tool: it installs
// @github/copilot via npm and launches it with Toby's generated MCP config and
// AGENTS.md instructions.
package copilot

import (
	"context"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	appconfig "petris.dev/toby/internal/config/app"
	copilotconfig "petris.dev/toby/internal/tools/builtin/copilot/config"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.copilot", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "copilot"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Copilot",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

type Params struct {
	fx.In

	SessionConfig *sessionconfig.Holder
	Sandbox       sandbox.Service
	ContextFiles  *contextfiles.Service
	Config        *appconfig.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(params Params) Result {
	svc := &copilotTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			[]string{"npm", "install", "-g", "@github/copilot"},
			nil,
		),
		sessionConfig: params.SessionConfig,
		contextFiles:  params.ContextFiles,
		config:        params.Config,
	}
	return Result{Service: svc}
}

type copilotTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	contextFiles  *contextfiles.Service
	config        *appconfig.Service
	yolo          bool
}

var _ tools.Tool = (*copilotTool)(nil)

func (t *copilotTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	t.yolo = t.config.YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *copilotTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}

	return t.Sandbox.PrependEnvironment(ctx, "COPILOT_CUSTOM_INSTRUCTIONS_DIRS", copilotconfig.InstructionsDir(), ",")
}

func (t *copilotTool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *copilotTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return copilotconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.sessionConfig.Get())
}

func (t *copilotTool) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	argv := append([]string{"copilot"}, contextFlags()...)
	if t.yolo {
		argv = append(argv, "--allow-all-tools")
	}
	argv = append(argv, extra...)
	return argv, nil
}

func contextFlags() []string {
	return []string{"--additional-mcp-config", "@" + copilotconfig.MCPConfigPath()}
}
