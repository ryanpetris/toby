// Package claude provides the Claude Code CLI agent tool: it installs
// @anthropic-ai/claude-code via npm and launches it with Toby's generated MCP,
// settings, and instruction files passed through launch flags.
package claude

import (
	"context"
	"path/filepath"
	"petris.dev/toby/container/layout"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	appconfig "petris.dev/toby/internal/config/app"
	claudeconfig "petris.dev/toby/internal/tools/builtin/claude/config"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.claude", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "claude"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Claude",
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
	svc := &claudeTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			[]string{"npm", "install", "-g", "@anthropic-ai/claude-code"},
			nil,
		),
		sessionConfig: params.SessionConfig,
		contextFiles:  params.ContextFiles,
		config:        params.Config,
	}
	return Result{Service: svc}
}

type claudeTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	contextFiles  *contextfiles.Service
	config        *appconfig.Service
	yolo          bool
}

var _ tools.Tool = (*claudeTool)(nil)

func (t *claudeTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	t.yolo = t.config.YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *claudeTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}

	return t.Sandbox.SetEnvironment(ctx, "CLAUDE_CONFIG_DIR", filepath.Join(layout.Home, ".config", "claude"))
}

func (t *claudeTool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *claudeTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return claudeconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.sessionConfig.Get())
}

// LaunchCommand builds the Claude Code command, injecting Toby's generated context
// files through launch flags while Claude keeps its normal writable config directory.
func (t *claudeTool) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	argv := append([]string{"claude"}, contextFlags()...)
	if t.yolo {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	argv = append(argv, extra...)
	return argv, nil
}

func contextFlags() []string {
	return []string{
		"--mcp-config", claudeconfig.StaticMcpPath,
		"--settings", claudeconfig.StaticSettingsPath,
		"--append-system-prompt-file", claudeconfig.StaticInstructionsPath,
	}
}
