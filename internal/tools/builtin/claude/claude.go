// Package claude provides the Claude Code CLI agent tool: it installs
// @anthropic-ai/claude-code via npm and launches it with Toby's generated MCP,
// settings, and instruction files passed through launch flags.
package claude

import (
	"path/filepath"

	sessionconfig "petris.dev/toby/internal/config/session"
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
	simple := kit.NewSimple(
		params.Sandbox,
		tools.Base{Metadata: Meta},
		[]string{".config", "claude"},
		[]string{"npm", "install", "-g", "--allow-scripts=@anthropic-ai/claude-code", "@anthropic-ai/claude-code"},
		map[string]string{
			"CLAUDE_CONFIG_DIR": filepath.Join(layout.Home, ".config", "claude"),
		},
	)
	simple.LaunchCommand = "claude"
	simple.LaunchArgs = nativeFlags()
	simple.YoloArgs = []string{"--dangerously-skip-permissions"}
	simple.Yolo = kit.YoloFromConfig(params.Config)
	svc := &claudeTool{
		Simple:        simple,
		sessionConfig: params.SessionConfig,
	}
	return result{Service: svc}
}

type claudeTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
}

var _ tools.Tool = (*claudeTool)(nil)
var _ toolfiles.Contributor = (*claudeTool)(nil)

func (t *claudeTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return claudeconfig.NativeFiles(Name, ownership, cfg)
}

func nativeFlags() []string {
	return []string{
		"--mcp-config", claudeconfig.NativeMCPPath,
		"--settings", claudeconfig.NativeSettingsPath,
	}
}
