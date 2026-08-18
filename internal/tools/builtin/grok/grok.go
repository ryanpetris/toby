// Package grok provides the Grok CLI agent tool: it installs the x.ai Grok CLI
// into the sandbox and launches it with Toby's generated plugin, config.toml
// enablement patch, and instructions.
package grok

import (
	"context"
	pathpkg "path"
	"path/filepath"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	grokconfig "petris.dev/toby/internal/tools/builtin/grok/config"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/kit"
	"petris.dev/toby/internal/tools/runtimepath"
)

const baseURL = "https://x.ai/cli"

// Name is this tool's canonical identifier.
const Name = "grok"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Grok",
	LaunchHelp:    "Launch Grok",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
}

const grokInstallPath = "grok/install.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &grokTool{Simple: &kit.Simple{
		Base:           tools.Base{Metadata: Meta},
		Sandbox:        params.Sandbox,
		SandboxSubpath: []string{".grok"},
	}, sessionConfig: params.SessionConfig}
	return result{Service: svc}
}

type grokTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
}

var _ tools.Tool = (*grokTool)(nil)
var _ runtimeassets.Contributor = (*grokTool)(nil)
var _ toolfiles.Contributor = (*grokTool)(nil)

func (t *grokTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return grokconfig.NativeFiles(Name, ownership, cfg)
}

func (t *grokTool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := grokFiles.ReadFile("resources/install.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, grokInstallPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *grokTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}

	return t.Sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".grok", "bin"), ":")
}

func (t *grokTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.Sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"grok",
		)
		if err != nil || exists {
			return err
		}
	}

	arch, err := kit.LinuxAssetArch("grok")
	if err != nil {
		return err
	}
	installer, err := runtimepath.Resolve(grokInstallPath)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(
		ctx,
		[]string{installer, baseURL, arch},
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

func (t *grokTool) Launch(ctx context.Context, extra []string) error {
	args, err := t.launchArgs(extra)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(ctx, append([]string{"grok"}, args...), sandbox.ExecOptions{Foreground: true})
	return err
}

func (t *grokTool) launchArgs(extra []string) ([]string, error) {
	args := []string{}
	if rules := grokconfig.Rules(
		t.sessionConfig.Snapshot().Instructions.Contents,
	); rules != "" {
		args = append(args, "--rules", rules)
	}
	args = append(args, extra...)
	return args, nil
}
