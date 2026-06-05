package npm

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

var Module = fx.Module("tools.npm", fx.Provide(Provide))

const npmSandboxInitPath = "npm/sandbox-init.sh"

//go:embed sandbox-init.sh
var npmFiles embed.FS

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	Paths        config.Paths
	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &npmTool{
		Base:         toolutil.Base(tools.NpmToolName, "Launch Node Package Manager", tools.GroupSystem, tools.GroupVCS),
		paths:        params.Paths,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type npmTool struct {
	tools.Base
	paths        config.Paths
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

func (t *npmTool) ConfigureSandbox(ctx context.Context) error {
	return helpers.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		home := layout.Home
		prefix := filepath.Join(home, ".local", "npm-global")
		cache := filepath.Join(home, ".cache", "npm")
		for key, value := range map[string]string{
			"NPM_CONFIG_PREFIX": prefix,
			"npm_config_prefix": prefix,
			"NPM_CONFIG_CACHE":  cache,
			"npm_config_cache":  cache,
		} {
			if err := t.sandbox.SetEnvironment(ctx, key, value); err != nil {
				return err
			}
		}
		return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(prefix, "bin"), ":")
	})
}

func (t *npmTool) InitSandbox(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		_, err := t.sandbox.Exec(ctx, []string{t.contextPath(npmSandboxInitPath)}, sandbox.ExecOptions{})
		return err
	})
}

func (t *npmTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := npmFiles.ReadFile("sandbox-init.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, npmSandboxInitPath, data, 0o755)
		return err
	})
}

func (t *npmTool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
}

func (t *npmTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"npm"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
