package emdash

import (
	"context"
	"embed"
	"path/filepath"

	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const appImageURL = "https://github.com/generalaction/emdash/releases/latest/download/emdash-x86_64.AppImage"

var Module = fx.Module("tools.emdash", fx.Provide(Provide))

const emdashInstallPath = "emdash/install.sh"

//go:embed install.sh
var emdashFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"emdash"`
	Registry tool.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &emdashTool{Base: toolutil.Base(tool.EmdashToolName, "Launch Emdash", tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS), sandbox: params.Sandbox, contextFiles: params.ContextFiles}
	return Result{Service: svc, Registry: svc}
}

type emdashTool struct {
	tool.Base
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *emdashTool) RegisterContextFiles(ctx context.Context, _ tool.ContextOptions) error {
	return tool.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := emdashFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, emdashInstallPath, data, 0o500)
		return err
	})
}

func (t *emdashTool) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *emdashTool) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *emdashTool) install(ctx context.Context, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, tool.ExecOptions{HideOutput: true}, "emdash")
			if err != nil || exists {
				return err
			}
		}
		_, err := t.sandbox.Exec(ctx, []string{t.contextPath(emdashInstallPath), appImageURL}, tool.ExecOptions{})
		return err
	})
}

func (t *emdashTool) contextPath(path string) string {
	return filepath.Join(t.sandbox.Paths().Context, filepath.FromSlash(path))
}

func (t *emdashTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"emdash"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}
