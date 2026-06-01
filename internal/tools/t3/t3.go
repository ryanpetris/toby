package t3

import (
	"context"
	"embed"
	"path/filepath"

	"petris.dev/toby/internal/config"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.t3", fx.Provide(Provide))

const t3WrapperPath = "t3/t3-wrapper.sh"

//go:embed t3-wrapper.sh
var t3Files embed.FS

type Params struct {
	fx.In

	Paths        config.Paths
	NPM          tool.Tool `name:"npm"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"t3"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	simple := toolutil.Simple(
		params.Paths,
		params.Sandbox,
		toolutil.Base(tool.T3ToolName, "Launch T3 Code", tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		nil,
		nil,
		[]string{"npm", "install", "-g", "t3"},
		map[string]string{"T3CODE_NO_BROWSER": "1"},
	)
	simple.InstallCheckCommand = "t3"
	svc := &t3Tool{
		Simple:       simple,
		npm:          params.NPM,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type t3Tool struct {
	*tool.Simple
	npm          tool.Tool
	contextFiles *contextfiles.Service
}

func (t *t3Tool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *t3Tool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *t3Tool) SandboxContextSetup(ctx context.Context) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxContextSetup(ctx)
}

func (t *t3Tool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		if registrar, ok := t.npm.(tool.ContextFileTool); ok {
			if err := registrar.RegisterContextFiles(ctx, opts); err != nil {
				return err
			}
		}
		data, err := t3Files.ReadFile("t3-wrapper.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, t3WrapperPath, data, 0o500)
		return err
	})
}

func (t *t3Tool) SandboxInit(ctx context.Context) error {
	if err := toolutil.SandboxInitDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx)
}

func (t *t3Tool) Install(ctx context.Context) error {
	if err := toolutil.InstallDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx)
}

func (t *t3Tool) Upgrade(ctx context.Context) error {
	if err := toolutil.UpgradeDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx)
}

func (t *t3Tool) Launch(ctx context.Context, extra []string) error {
	path := filepath.Join(t.Sandbox.Paths().Context, filepath.FromSlash(t3WrapperPath))
	_, err := t.Sandbox.Exec(ctx, append([]string{path}, extra...), tool.ExecOptions{Foreground: true})
	return err
}
