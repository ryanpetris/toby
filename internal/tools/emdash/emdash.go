package emdash

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"

	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const appImageURL = "https://github.com/generalaction/emdash/releases/latest/download/emdash-x86_64.AppImage"

var Module = fx.Module("tools.emdash", fx.Provide(Provide))

const emdashInstallPath = "emdash/install"

//go:embed install
var emdashFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"emdash"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide() Result {
	svc := &emdashTool{Base: toolutil.Base(tool.EmdashToolName, "Launch Emdash", tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS)}
	return Result{Service: svc, Registry: svc}
}

type emdashTool struct{ tool.Base }

func (t *emdashTool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	data, err := emdashFiles.ReadFile("install")
	if err != nil {
		return err
	}
	return run.ContextFiles.AddBytes(emdashInstallPath, data, 0o500)
}

func (t *emdashTool) Install(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, false)
}

func (t *emdashTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, true)
}

func (t *emdashTool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "emdash")
			if err != nil || exists {
				return err
			}
		}
		path, err := emdashInstallLaunchPath(run)
		if err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{path, appImageURL}, tool.ExecOptions{})
	})
}

func emdashInstallLaunchPath(run *tool.RunContext) (string, error) {
	contextDir := ""
	if run != nil {
		if run.ContextFiles != nil {
			contextDir = run.ContextFiles.ContextDir()
		}
		if contextDir == "" && run.Sandbox != nil {
			contextDir = run.Sandbox.TobyContextDir()
		}
	}
	if contextDir == "" {
		return "", fmt.Errorf("sandbox context directory is not configured")
	}
	return filepath.Join(contextDir, filepath.FromSlash(emdashInstallPath)), nil
}

func (t *emdashTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"emdash"}, run.Extra...), tool.ExecOptions{})
}
