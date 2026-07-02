// Package grok provides the Grok CLI agent tool: it installs the x.ai Grok CLI
// into the sandbox and launches it with Toby's generated MCP config and
// instructions.
package grok

import (
	"context"
	"log"
	"path/filepath"
	"petris.dev/toby/container/layout"

	"petris.dev/toby/config/session"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/exitcode"
	grokconfig "petris.dev/toby/internal/tools/builtin/grok/config"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

const baseURL = "https://x.ai/cli"

var Module = fx.Module("tools.grok", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "grok"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Grok",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
}

const grokInstallPath = layout.Scripts + "/grok/install.sh"

type Params struct {
	fx.In

	SessionConfig *sessionconfig.Holder
	Sandbox       sandbox.Service
	ContextFiles  *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(params Params) Result {
	svc := &grokTool{Simple: &kit.Simple{
		Base:    tools.Base{Metadata: Meta},
		Sandbox: params.Sandbox,
	}, sessionConfig: params.SessionConfig, contextFiles: params.ContextFiles}
	return Result{Service: svc}
}

type grokTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	contextFiles  *contextfiles.Service
}

var _ tools.Tool = (*grokTool)(nil)

func (t *grokTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := grokFiles.ReadFile("resources/install.sh")
	if err != nil {
		return err
	}
	if _, err := t.contextFiles.AddFile(ctx, grokInstallPath, data, 0o755); err != nil {
		return err
	}

	return grokconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.sessionConfig.Get())
}

func (t *grokTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}

	return t.Sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".grok", "bin"), ":")
}

func (t *grokTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.Sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "grok")
		if err != nil || exists {
			return err
		}
	}

	arch, err := kit.LinuxAssetArch("grok")
	if err != nil {
		log.Printf("%s", err)
		return exitcode.Code(1)
	}
	_, err = t.Sandbox.Exec(ctx, []string{grokInstallPath, baseURL, arch}, sandbox.ExecOptions{})
	return err
}

func (t *grokTool) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	args, err := t.launchArgs(extra)
	if err != nil {
		return nil, err
	}
	return append([]string{"grok"}, args...), nil
}

func (t *grokTool) launchArgs(extra []string) ([]string, error) {
	args := []string{}
	if rules := grokconfig.Rules(t.sessionConfig.Get().Instructions.Contents); rules != "" {
		args = append(args, "--rules", rules)
	}
	args = append(args, extra...)
	return args, nil
}
