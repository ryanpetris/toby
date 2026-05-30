package t3

import (
	"context"
	"embed"
	"fmt"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.t3", fx.Provide(Provide))

const t3WrapperPath = "t3/t3-wrapper"

//go:embed t3-wrapper
var t3Files embed.FS

type Params struct {
	fx.In

	Paths config.Paths
	NPM   tool.Tool `name:"npm"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"t3"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	simple := toolutil.Simple(
		params.Paths,
		toolutil.Base(tool.T3ToolName, "Launch T3 Code", tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		nil,
		nil,
		[]string{"npm", "install", "-g", "t3"},
		map[string]string{"T3CODE_NO_BROWSER": "1"},
	)
	simple.InstallCheckCommand = "t3"
	svc := &t3Tool{
		Simple: simple,
		npm:    params.NPM,
	}
	return Result{Service: svc, Registry: svc}
}

type t3Tool struct {
	*tool.Simple
	npm tool.Tool
}

func (t *t3Tool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *t3Tool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), t.Simple.Binds())
}

func (t *t3Tool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), t.Simple.PathEntries())
}

func (t *t3Tool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	return t.Simple.HostInit(ctx, opts)
}

func (t *t3Tool) SandboxContextSetup(ctx *tool.RunContext) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxContextSetup(ctx)
}

func (t *t3Tool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	data, err := t3Files.ReadFile("t3-wrapper")
	if err != nil {
		return err
	}
	return run.ContextFiles.AddBytes(t3WrapperPath, data, 0o500)
}

func (t *t3Tool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.SandboxInitDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.SandboxInit(ctx, run)
}

func (t *t3Tool) Install(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.InstallDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Install(ctx, run)
}

func (t *t3Tool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.UpgradeDependencies(ctx, run, t.npm); err != nil {
		return err
	}
	return t.Simple.Upgrade(ctx, run)
}

func (t *t3Tool) Launch(ctx context.Context, run *tool.RunContext) error {
	path, err := t3WrapperLaunchPath(run)
	if err != nil {
		return err
	}
	return tool.RunCommand(ctx, run.Launch, append([]string{path}, run.Extra...), tool.ExecOptions{})
}

func t3WrapperLaunchPath(run *tool.RunContext) (string, error) {
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
	return filepath.Join(contextDir, filepath.FromSlash(t3WrapperPath)), nil
}
