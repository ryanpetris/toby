// Package claude provides the Claude Code CLI agent tool: it installs
// @anthropic-ai/claude-code via npm and launches it with Toby's generated MCP,
// settings, and instruction files passed through launch flags.
package claude

import (
	"context"
	"path/filepath"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	claudeconfig "petris.dev/toby/internal/tools/builtin/claude/config"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/kit"
)

// Name is this tool's canonical identifier.
const Name = "claude"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Claude",
	LaunchHelp:    "Launch Claude",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &claudeTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			[]string{".config", "claude"},
			[]string{"npm", "install", "-g", "--allow-scripts=@anthropic-ai/claude-code", "@anthropic-ai/claude-code"},
			nil,
		),
		sessionConfig: params.SessionConfig,
		config:        params.Config,
	}
	return result{Service: svc}
}

type claudeTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	config        *appconfig.LaunchHolder
	yolo          bool
}

var _ tools.Tool = (*claudeTool)(nil)
var _ toolfiles.Contributor = (*claudeTool)(nil)

func (t *claudeTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	current := t.config.Current()
	t.yolo = current != nil && current.Settings().YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *claudeTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}

	return t.Sandbox.SetEnvironment(ctx, "CLAUDE_CONFIG_DIR", filepath.Join(layout.Home, ".config", "claude"))
}

func (t *claudeTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return claudeconfig.NativeFiles(Name, ownership, cfg)
}

// Launch starts Claude Code with Toby's generated MCP and settings files while
// Claude keeps its normal writable config directory.
func (t *claudeTool) Launch(ctx context.Context, extra []string) error {
	argv := append([]string{"claude"}, nativeFlags()...)
	if t.yolo {
		argv = append(argv, "--dangerously-skip-permissions")
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
	return err
}

func nativeFlags() []string {
	return []string{
		"--mcp-config", claudeconfig.NativeMCPPath,
		"--settings", claudeconfig.NativeSettingsPath,
	}
}
