package npm

import (
	"context"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.npm", fx.Provide(Provide))

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
		script := `if ! command -v npm >/dev/null 2>&1; then printf "npm is not available inside the sandbox\n" >&2; exit 127; fi; if [ -d "$NPM_CONFIG_PREFIX/bin" ] && [ -d "$NPM_CONFIG_PREFIX/lib/node_modules" ]; then exit 0; fi; mkdir -p "$NPM_CONFIG_PREFIX/bin" "$NPM_CONFIG_PREFIX/lib/node_modules" "$NPM_CONFIG_CACHE"`
		return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
	})
}

func (t *npmTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"npm"}, run.Extra...), tool.ExecOptions{})
}
