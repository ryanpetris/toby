package emdash

import (
	"context"
	"embed"
	"path/filepath"
	"petris.dev/toby/container/layout"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/internal/dirty/tools/helpers"
	"petris.dev/toby/internal/dirty/tools/toolutil"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

const appImageURL = "https://github.com/generalaction/emdash/releases/latest/download/emdash-x86_64.AppImage"

var Module = fx.Module("tools.emdash", fx.Provide(Provide))

const emdashInstallPath = "emdash/install.sh"

//go:embed install.sh
var emdashFiles embed.FS

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &emdashTool{Base: toolutil.Base(tools.EmdashToolName, "Launch Emdash", tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS), sandbox: params.Sandbox, contextFiles: params.ContextFiles}
	return Result{Service: svc}
}

type emdashTool struct {
	tools.Base
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

func (t *emdashTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := emdashFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, emdashInstallPath, data, 0o755)
		return err
	})
}

func (t *emdashTool) Install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "emdash")
			if err != nil || exists {
				return err
			}
		}
		_, err := t.sandbox.Exec(ctx, []string{t.contextPath(emdashInstallPath), appImageURL}, sandbox.ExecOptions{})
		return err
	})
}

func (t *emdashTool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
}

func (t *emdashTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"emdash"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
