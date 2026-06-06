// Package codex provides the OpenAI Codex CLI agent tool: it installs
// @openai/codex via npm and launches it with Toby's per-session MCP servers and
// instructions injected through -c config overrides.
package codex

import (
	"context"

	"petris.dev/toby/config/session"
	appconfig "petris.dev/toby/internal/config/app"
	codexconfig "petris.dev/toby/internal/tools/builtin/codex/config"
	"petris.dev/toby/internal/tools/builtin/npm"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.codex", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "codex"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Codex",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

type Params struct {
	fx.In

	SessionConfig *sessionconfig.Holder
	Sandbox       sandbox.Service
	Config        *appconfig.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(params Params) Result {
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
	return Result{Service: svc}
}

type codexTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	config        *appconfig.Service
	yolo          bool
}

var _ tools.Tool = (*codexTool)(nil)

func (t *codexTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	t.yolo = t.config.YoloEnabled()

	return t.Simple.PrepareHost(ctx, opts)
}

func (t *codexTool) ConfigureSandbox(ctx context.Context) error {
	return t.Simple.ConfigureSandbox(ctx)
}

func (t *codexTool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *codexTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return nil
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
	args, err := codexconfig.ConfigArgs(t.sessionConfig.Get())
	if err != nil {
		return nil, err
	}
	if t.yolo {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	args = append(args, extra...)
	return args, nil
}
