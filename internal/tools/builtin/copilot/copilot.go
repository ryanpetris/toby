// Package copilot provides the GitHub Copilot CLI agent tool with Toby's
// generated MCP configuration and instructions.
package copilot

import (
	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	copilotconfig "petris.dev/toby/internal/tools/builtin/copilot/config"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/kit"
)

// Name is this tool's canonical identifier.
const Name = "copilot"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Copilot",
	LaunchHelp:    "Launch Copilot",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

// provide constructs the tool implementation.
func provide(params params) result {
	simple := kit.NewSimple(
		params.Sandbox,
		tools.Base{Metadata: Meta},
		[]string{".copilot"},
		[]string{"npm", "install", "-g", "--allow-scripts=@github/copilot", "@github/copilot"},
		nil,
	)
	simple.LaunchCommand = "copilot"
	simple.LaunchArgs = []string{
		"--additional-mcp-config",
		"@" + copilotconfig.NativeMCPPath,
	}
	simple.YoloArgs = []string{"--allow-all-tools"}
	simple.Yolo = kit.YoloFromConfig(params.Config)
	svc := &copilotTool{
		Simple:        simple,
		sessionConfig: params.SessionConfig,
	}
	return result{Service: svc}
}

type copilotTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
}

var _ tools.Tool = (*copilotTool)(nil)
var _ toolfiles.Contributor = (*copilotTool)(nil)

func (t *copilotTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return copilotconfig.NativeFiles(Name, ownership, cfg)
}
