// Package npm provides the Node Package Manager tool — the Node.js/npm runtime
// the npm-installed agent tools depend on.
package npm

import (
	"context"
	"path/filepath"
	"petris.dev/toby/container/layout"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.npm", fx.Provide(Provide))

// Name is this tool's canonical identifier (the dependency name npm-installed
// agent tools reference).
const Name = "npm"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Node Package Manager",
	Group:         tools.GroupSystem,
	ContextGroups: []string{tools.GroupSystem, tools.GroupVCS},
}

const npmSandboxInitPath = layout.Scripts + "/npm/sandbox-init.sh"

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
		Base:         tools.Base{Metadata: Meta},
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
	_, err := t.sandbox.Exec(ctx, []string{npmSandboxInitPath}, sandbox.ExecOptions{})
	return err
}

func (t *npmTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := npmFiles.ReadFile("resources/sandbox-init.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, npmSandboxInitPath, data, 0o755)
	return err
}

func (t *npmTool) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	return append([]string{"npm"}, extra...), nil
}
