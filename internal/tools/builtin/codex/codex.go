// Package codex provides the OpenAI Codex CLI agent tool with Toby's generated
// MCP configuration and instructions.
package codex

import (
	"context"

	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	codexconfig "petris.dev/toby/internal/tools/builtin/codex/config"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/internal/tools/kit"
)

// Name is this tool's canonical identifier.
const Name = "codex"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Codex",
	LaunchHelp:    "Launch Codex",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &codexTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			[]string{".codex"},
			[]string{"npm", "install", "-g", "@openai/codex"},
			nil,
		),
		sessionConfig: params.SessionConfig,
		config:        params.Config,
	}
	return result{Service: svc}
}

type codexTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	config        *appconfig.LaunchHolder
	yolo          bool
}

var _ tools.Tool = (*codexTool)(nil)
var _ toolfiles.Contributor = (*codexTool)(nil)

func (t *codexTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	current := t.config.Current()
	t.yolo = current != nil && current.Settings().YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *codexTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return codexconfig.NativeFiles(Name, ownership, cfg)
}

func (t *codexTool) Launch(ctx context.Context, extra []string) error {
	args, err := t.launchArgs(extra)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(ctx, append([]string{"codex"}, args...), sandbox.ExecOptions{Foreground: true})
	return err
}

func (t *codexTool) launchArgs(extra []string) ([]string, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}
	args, err := codexconfig.MCPConfigArgs(cfg)
	if err != nil {
		return nil, err
	}
	if t.yolo {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, extra...)
	return args, nil
}
