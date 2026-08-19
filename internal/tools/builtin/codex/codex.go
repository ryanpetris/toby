// Package codex provides the OpenAI Codex CLI agent tool with Toby's generated
// MCP configuration and instructions.
package codex

import (
	"context"

	sessionconfig "petris.dev/toby/internal/config/session"
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
	simple := kit.NewSimple(
		params.Sandbox,
		tools.Base{Metadata: Meta},
		[]string{".codex"},
		[]string{"npm", "install", "-g", "--allow-scripts=@openai/codex", "@openai/codex"},
		nil,
	)
	simple.Yolo = kit.YoloFromConfig(params.Config)
	svc := &codexTool{
		Simple:        simple,
		sessionConfig: params.SessionConfig,
	}
	return result{Service: svc}
}

type codexTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
}

var _ tools.Tool = (*codexTool)(nil)
var _ toolfiles.Contributor = (*codexTool)(nil)

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
	if t.YoloEnabled() {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, extra...)
	return args, nil
}
