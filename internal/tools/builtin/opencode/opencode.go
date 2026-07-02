// Package opencode provides the OpenCode agent tool: it installs opencode-ai via
// npm and launches it with Toby's generated opencode.json (MCP servers,
// providers, instructions, and permission paths).
package opencode

import (
	"context"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/tools/builtin/npm"
	opencodeconfig "petris.dev/toby/internal/tools/builtin/opencode/config"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.opencode", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "opencode"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch OpenCode",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

type Params struct {
	fx.In

	SessionConfig *sessionconfig.Holder
	Sandbox       sandbox.Service
	ContextFiles  *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(params Params) Result {
	svc := &openCodeTool{
		Base:          tools.Base{Metadata: Meta},
		sessionConfig: params.SessionConfig,
		sandbox:       params.Sandbox,
		contextFiles:  params.ContextFiles,
	}
	return Result{Service: svc}
}

type openCodeTool struct {
	tools.Base
	sessionConfig *sessionconfig.Holder
	sandbox       sandbox.Service
	contextFiles  *contextfiles.Service
}

var _ tools.Tool = (*openCodeTool)(nil)

func (t *openCodeTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return opencodeconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.sessionConfig.Get())
}

func (t *openCodeTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "opencode")
		if err != nil || exists {
			return err
		}
	}
	_, err := t.sandbox.Exec(ctx, []string{"npm", "install", "-g", "opencode-ai"}, sandbox.ExecOptions{})
	return err
}

func (t *openCodeTool) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	return append([]string{"opencode"}, extra...), nil
}
