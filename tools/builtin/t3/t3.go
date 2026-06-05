// Package t3 provides the T3 Code agent tool, installed via npm and launched
// through a wrapper script.
package t3

import (
	"context"
	"path/filepath"
	"petris.dev/toby/container/layout"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/builtin/npm"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.t3", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "t3"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch T3 Code",
	Group:         tools.GroupUI,
	ContextGroups: []string{tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

const t3WrapperPath = "t3/t3-wrapper.sh"

type Params struct {
	fx.In

	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(params Params) Result {
	simple := kit.NewSimple(
		params.Sandbox,
		tools.Base{Metadata: Meta},
		nil,
		[]string{"npm", "install", "-g", "t3"},
		map[string]string{"T3CODE_NO_BROWSER": "1"},
	)
	simple.InstallCheckCommand = "t3"
	svc := &t3Tool{
		Simple:       simple,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type t3Tool struct {
	*kit.Simple
	contextFiles *contextfiles.Service
}

var _ tools.Tool = (*t3Tool)(nil)

func (t *t3Tool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	return t.Simple.PrepareHost(ctx, opts)
}

func (t *t3Tool) ConfigureSandbox(ctx context.Context) error {
	return t.Simple.ConfigureSandbox(ctx)
}

func (t *t3Tool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	data, err := t3Files.ReadFile("resources/t3-wrapper.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, t3WrapperPath, data, 0o755)
	return err
}

func (t *t3Tool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *t3Tool) Launch(ctx context.Context, extra []string) error {
	path := filepath.Join(layout.Context, filepath.FromSlash(t3WrapperPath))
	_, err := t.Sandbox.Exec(ctx, append([]string{path}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
