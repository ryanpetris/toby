package t3

import (
	"context"
	"embed"
	"path/filepath"
	"petris.dev/toby/container/layout"

	"petris.dev/toby/config"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.t3", fx.Provide(Provide))

const t3WrapperPath = "t3/t3-wrapper.sh"

//go:embed t3-wrapper.sh
var t3Files embed.FS

type Params struct {
	fx.In

	Paths        config.Paths
	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	simple := toolutil.NewSimple(
		params.Paths,
		params.Sandbox,
		toolutil.DependentBase(tools.T3ToolName, "Launch T3 Code", 100, []string{tools.NpmToolName}, tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS),
		nil,
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
	*toolutil.Simple
	contextFiles *contextfiles.Service
}

func (t *t3Tool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	return t.Simple.PrepareHost(ctx, opts)
}

func (t *t3Tool) ConfigureSandbox(ctx context.Context) error {
	return t.Simple.ConfigureSandbox(ctx)
}

func (t *t3Tool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := t3Files.ReadFile("t3-wrapper.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, t3WrapperPath, data, 0o755)
		return err
	})
}

func (t *t3Tool) InitSandbox(ctx context.Context) error {
	return t.Simple.InitSandbox(ctx)
}

func (t *t3Tool) Launch(ctx context.Context, extra []string) error {
	path := filepath.Join(layout.Context, filepath.FromSlash(t3WrapperPath))
	_, err := t.Sandbox.Exec(ctx, append([]string{path}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
