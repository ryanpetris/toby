package npm

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

var Module = fx.Module("tools.npm", fx.Provide(Provide))

const npmSandboxInitPath = "npm/sandbox-init"

//go:embed sandbox-init
var npmFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"npm"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(paths config.Paths) Result {
	svc := &npmTool{
		Base:  toolutil.Base(tool.NpmToolName, "Launch Node Package Manager", tool.GroupSystem, tool.GroupVCS),
		paths: paths,
	}
	return Result{Service: svc, Registry: svc}
}

type npmTool struct {
	tool.Base
	paths config.Paths
}

func (t *npmTool) PathEntries() []tool.PathTarget {
	return []tool.PathTarget{tool.HomeTarget(".local", "npm-global", "bin")}
}

func (t *npmTool) SandboxContextSetup(ctx *tool.RunContext) error {
	return tool.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		prefix := filepath.Join(ctx.Sandbox.HomeDir(), ".local", "npm-global")
		cache := filepath.Join(ctx.Sandbox.HomeDir(), ".cache", "npm")
		ctx.Env["NPM_CONFIG_PREFIX"] = prefix
		ctx.Env["npm_config_prefix"] = prefix
		ctx.Env["NPM_CONFIG_CACHE"] = cache
		ctx.Env["npm_config_cache"] = cache
		return nil
	})
}

func (t *npmTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return tool.SandboxInitOnce(run, t.Name(), func() error {
		path, err := npmSandboxInitLaunchPath(run)
		if err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{path}, tool.ExecOptions{})
	})
}

func (t *npmTool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	data, err := npmFiles.ReadFile("sandbox-init")
	if err != nil {
		return err
	}
	return run.ContextFiles.AddBytes(npmSandboxInitPath, data, 0o500)
}

func npmSandboxInitLaunchPath(run *tool.RunContext) (string, error) {
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
	return filepath.Join(contextDir, filepath.FromSlash(npmSandboxInitPath)), nil
}

func (t *npmTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"npm"}, run.Extra...), tool.ExecOptions{})
}
