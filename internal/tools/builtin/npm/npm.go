// Package npm provides the Node Package Manager tool — the Node.js/npm runtime
// the npm-installed agent tools depend on.
package npm

import (
	"context"
	pathpkg "path"
	"path/filepath"

	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/runtimepath"
)

// Name is this tool's canonical identifier (the dependency name npm-installed
// agent tools reference).
const Name = "npm"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "NPM",
	LaunchHelp:    "Launch Node Package Manager",
	Group:         tools.GroupSystem,
	ContextGroups: []string{tools.GroupSystem, tools.GroupVCS},
}

const npmSandboxInitPath = "npm/sandbox-init.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &npmTool{
		Base:    tools.Base{Metadata: Meta},
		sandbox: params.Sandbox,
	}
	return result{Service: svc}
}

type npmTool struct {
	tools.Base
	sandbox sandbox.Service
}

var _ tools.Tool = (*npmTool)(nil)
var _ runtimeassets.Contributor = (*npmTool)(nil)

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
	assetPath, err := runtimepath.Resolve(npmSandboxInitPath)
	if err != nil {
		return err
	}
	_, err = t.sandbox.Exec(
		ctx,
		[]string{assetPath},
		sandbox.ExecOptions{Status: "Preparing storage"},
	)
	return err
}

func (t *npmTool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := npmFiles.ReadFile("resources/sandbox-init.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, npmSandboxInitPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *npmTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"npm"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
