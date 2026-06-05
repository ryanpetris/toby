// Package npm provides the Node Package Manager tool — the Node.js/npm runtime
// the npm-installed agent tools depend on.
package npm

import (
	"context"
	"embed"
	"path/filepath"
	"petris.dev/toby/container/layout"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.npm", fx.Provide(Provide))

const npmSandboxInitPath = "npm/sandbox-init.sh"

//go:embed sandbox-init.sh
var npmFiles embed.FS

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

type Params struct {
	fx.In

	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &npmTool{
		Base:         kit.Base(tools.NpmToolName, "Launch Node Package Manager", tools.GroupSystem, tools.GroupVCS),
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type npmTool struct {
	tools.Base
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

var _ tools.Tool = (*npmTool)(nil)

func (t *npmTool) ConfigureSandbox(ctx context.Context) error {
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
}

func (t *npmTool) InitSandbox(ctx context.Context) error {
	_, err := t.sandbox.Exec(ctx, []string{t.contextPath(npmSandboxInitPath)}, sandbox.ExecOptions{})
	return err
}

func (t *npmTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := npmFiles.ReadFile("sandbox-init.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, npmSandboxInitPath, data, 0o755)
	return err
}

func (t *npmTool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
}

func (t *npmTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"npm"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
