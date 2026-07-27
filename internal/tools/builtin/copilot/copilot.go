// Package copilot provides the GitHub Copilot CLI agent tool with Toby's
// generated MCP configuration and instructions.
package copilot

import (
	"context"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox"
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
	svc := &copilotTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			[]string{".copilot"},
			[]string{"npm", "install", "-g", "@github/copilot"},
			nil,
		),
		sessionConfig: params.SessionConfig,
		config:        params.Config,
	}
	return result{Service: svc}
}

type copilotTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	config        *appconfig.LaunchHolder
	yolo          bool
}

var _ tools.Tool = (*copilotTool)(nil)
var _ toolfiles.Contributor = (*copilotTool)(nil)

func (t *copilotTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	current := t.config.Current()
	t.yolo = current != nil && current.Settings().YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *copilotTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return copilotconfig.NativeFiles(Name, ownership, cfg)
}

func (t *copilotTool) Launch(ctx context.Context, extra []string) error {
	flags := []string{
		"--additional-mcp-config",
		"@" + copilotconfig.NativeMCPPath,
	}

	argv := append([]string{"copilot"}, flags...)
	if t.yolo {
		argv = append(argv, "--allow-all-tools")
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
	return err
}
