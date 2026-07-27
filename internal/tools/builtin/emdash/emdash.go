// Package emdash provides the Emdash AI agent tool, installed into the sandbox
// from a bundled install script.
package emdash

import (
	"context"
	pathpkg "path"
	"path/filepath"

	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/runtimepath"
)

const appImageURL = "https://github.com/generalaction/emdash/releases/latest/download/emdash-x86_64.AppImage"

// Name is this tool's canonical identifier.
const Name = "emdash"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Emdash",
	LaunchHelp:    "Launch Emdash",
	Group:         tools.GroupUI,
	ContextGroups: []string{tools.GroupUI, tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
}

const emdashInstallPath = "emdash/install.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &emdashTool{Base: tools.Base{Metadata: Meta}, sandbox: params.Sandbox}
	return result{Service: svc}
}

type emdashTool struct {
	tools.Base
	sandbox sandbox.Service
}

var _ tools.Tool = (*emdashTool)(nil)
var _ runtimeassets.Contributor = (*emdashTool)(nil)

func (t *emdashTool) ConfigureSandbox(ctx context.Context) error {
	return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".local", "bin"), ":")
}

func (t *emdashTool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := emdashFiles.ReadFile("resources/install.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, emdashInstallPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *emdashTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"emdash",
		)
		if err != nil || exists {
			return err
		}
	}

	installer, err := runtimepath.Resolve(emdashInstallPath)
	if err != nil {
		return err
	}
	_, err = t.sandbox.Exec(
		ctx,
		[]string{installer, appImageURL},
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

func (t *emdashTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"emdash"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
