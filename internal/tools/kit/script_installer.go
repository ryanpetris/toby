package kit

// ScriptInstaller installs a CLI from a runtime install script and a resolved
// release archive URL.

import (
	"context"
	"fmt"
	pathpkg "path"
	"path/filepath"

	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/runtimepath"
)

// ScriptInstaller mounts ~/.local/bin, installs from a bundled script, and
// launches the named command.
type ScriptInstaller struct {
	tools.Base
	Sandbox        sandbox.Service
	Command        string
	InstallRelPath string
	LoadScript     func() ([]byte, error)
	ArchiveURL     func(context.Context) (string, error)
}

var _ tools.Tool = (*ScriptInstaller)(nil)
var _ runtimeassets.Contributor = (*ScriptInstaller)(nil)

// ConfigureSandbox adds ~/.local/bin to PATH.
func (t *ScriptInstaller) ConfigureSandbox(ctx context.Context) error {
	return t.Sandbox.AppendEnvironment(
		ctx,
		"PATH",
		filepath.Join(layout.Home, ".local", "bin"),
		":",
	)
}

// RuntimeAssets publishes the install script into the sandbox runtime.
func (t *ScriptInstaller) RuntimeAssets() ([]runtimeassets.Asset, error) {
	if t.LoadScript == nil {
		return nil, fmt.Errorf("install script loader is required")
	}
	data, err := t.LoadScript()
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, t.InstallRelPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

// Install runs the bundled installer when the command is missing.
func (t *ScriptInstaller) Install(ctx context.Context, force bool) error {
	command := t.Command
	if command == "" {
		command = t.Name()
	}
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.Sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			command,
		)
		if err != nil || exists {
			return err
		}
	}
	if t.ArchiveURL == nil {
		return fmt.Errorf("archive URL resolver is required")
	}
	archiveURL, err := t.ArchiveURL(ctx)
	if err != nil {
		return err
	}
	installer, err := runtimepath.Resolve(t.InstallRelPath)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(
		ctx,
		[]string{installer, archiveURL},
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

// Launch runs the installed command.
func (t *ScriptInstaller) Launch(ctx context.Context, extra []string) error {
	command := t.Command
	if command == "" {
		command = t.Name()
	}
	_, err := t.Sandbox.Exec(
		ctx,
		append([]string{command}, extra...),
		sandbox.ExecOptions{Foreground: true},
	)
	return err
}
