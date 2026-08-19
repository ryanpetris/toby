// Package opencode provides the OpenCode agent tool: it installs opencode-ai via
// npm and launches it with Toby's generated opencode.json (MCP servers,
// providers, instructions, and permission paths).
package opencode

import (
	"path/filepath"

	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/builtin/npm"
	opencodeconfig "petris.dev/toby/internal/tools/builtin/opencode/config"
	"petris.dev/toby/internal/tools/kit"
)

// Name is this tool's canonical identifier.
const Name = "opencode"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "OpenCode",
	LaunchHelp:    "Launch OpenCode",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

// provide constructs the tool implementation.
func provide(params params) result {
	simple := kit.NewSimple(
		params.Sandbox,
		tools.Base{Metadata: Meta},
		nil,
		[]string{"npm", "install", "-g", "--allow-scripts=opencode-ai", "opencode-ai"},
		map[string]string{
			"OPENCODE_CONFIG_DIR": filepath.Dir(opencodeconfig.NativePriorityConfigPath),
		},
	)
	simple.ExtraMounts = []mount.Request{
		{Key: mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "config"}, Target: "~/.config/opencode", Access: mount.AccessRegular},
		{Key: mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "data"}, Target: "~/.local/share/opencode", Access: mount.AccessRegular},
	}
	simple.LaunchCommand = "opencode"
	svc := &openCodeTool{
		Simple:        simple,
		sessionConfig: params.SessionConfig,
	}
	return result{Service: svc}
}

type openCodeTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
}

var _ tools.Tool = (*openCodeTool)(nil)
var _ toolfiles.Contributor = (*openCodeTool)(nil)

func (t *openCodeTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return opencodeconfig.NativeFiles(Name, ownership, cfg)
}
