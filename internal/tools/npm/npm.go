package npm

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

var Module = fx.Module("tools.npm", fx.Provide(Provide))

const npmSandboxInitPath = "npm/sandbox-init.sh"

//go:embed sandbox-init.sh
var npmFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"npm"`
	Registry tool.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	Paths        config.Paths
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &npmTool{
		Base:         toolutil.Base(tool.NpmToolName, "Launch Node Package Manager", tool.GroupSystem, tool.GroupVCS),
		paths:        params.Paths,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type npmTool struct {
	tool.Base
	paths        config.Paths
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *npmTool) SandboxContextSetup(ctx context.Context) error {
	return helpers.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		home := t.sandbox.Paths().Home
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

func (t *npmTool) SandboxInit(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		_, err := t.sandbox.Exec(ctx, []string{t.contextPath(npmSandboxInitPath)}, tool.ExecOptions{})
		return err
	})
}

func (t *npmTool) RegisterContextFiles(ctx context.Context, _ tool.ContextOptions) error {
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
	return filepath.Join(t.sandbox.Paths().Context, filepath.FromSlash(path))
}

func (t *npmTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"npm"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}
