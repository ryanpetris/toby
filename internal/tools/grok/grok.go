package grok

import (
	"context"
	"embed"
	"log"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/diagnostic/exitcode"
	grokconfig "petris.dev/toby/internal/tools/grok/config"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const baseURL = "https://x.ai/cli"

var Module = fx.Module("tools.grok", fx.Provide(Provide))

const grokInstallPath = "grok/install.sh"

//go:embed install.sh
var grokFiles embed.FS

type Params struct {
	fx.In

	Paths        config.Paths
	Config       *tobyconfig.Service `optional:"true"`
	Proxy        *httpproxy.Service  `optional:"true"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &grokTool{Simple: &tool.Simple{
		Base:           toolutil.Base(tool.GrokToolName, "Launch Grok", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		Sandbox:        params.Sandbox,
		RootDir:        params.Paths.SandboxRoot,
		HostSubpath:    []string{".grok"},
		SandboxSubpath: []string{".grok"},
	}, config: params.Config, contextFiles: params.ContextFiles}
	svc.proxy = params.Proxy
	return Result{Service: svc}
}

type grokTool struct {
	*tool.Simple
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	contextFiles *contextfiles.Service
}

func (t *grokTool) RegisterContextFiles(ctx context.Context, _ tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := grokFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		if _, err := t.contextFiles.AddFile(ctx, grokInstallPath, data, 0o755); err != nil {
			return err
		}
		controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
		return grokconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy)
	})
}

func (t *grokTool) SandboxContextSetup(ctx context.Context) error {
	if err := t.Simple.SandboxContextSetup(ctx); err != nil {
		return err
	}
	return helpers.SandboxContextSetupOnce(ctx, t.Name()+".path", func() error {
		return t.Sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(t.Sandbox.Paths().Home, ".grok", "bin"), ":")
	})
}

func (t *grokTool) SandboxInit(ctx context.Context) error {
	if err := t.Simple.SandboxInit(ctx); err != nil {
		return err
	}
	return helpers.SandboxInitOnce(ctx, t.Name()+".managed-config", func() error {
		contextDir := t.Sandbox.Paths().Context
		home := t.Sandbox.Paths().Home
		grokHome := filepath.Join(home, ".grok")
		if err := t.Sandbox.MkdirOwned(ctx, grokHome, 0o755, control.HostUser, control.HostGroup); err != nil {
			return err
		}
		return t.Sandbox.SymlinkOwned(ctx, filepath.Join(grokHome, "managed_config.toml"), grokconfig.ConfigPath(contextDir), control.HostUser, control.HostGroup)
	})
}

func (t *grokTool) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *grokTool) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *grokTool) install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.Sandbox.Exec, tool.ExecOptions{HideOutput: true}, "grok")
			if err != nil || exists {
				return err
			}
		}
		arch, err := toolutil.LinuxAssetArch("grok")
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		_, err = t.Sandbox.Exec(ctx, []string{t.contextPath(grokInstallPath), baseURL, arch}, tool.ExecOptions{})
		return err
	})
}

func (t *grokTool) contextPath(path string) string {
	return filepath.Join(t.Sandbox.Paths().Context, filepath.FromSlash(path))
}

func (t *grokTool) Launch(ctx context.Context, extra []string) error {
	args, err := t.launchArgs(extra)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(ctx, append([]string{"grok"}, args...), tool.ExecOptions{Foreground: true})
	return err
}

func (t *grokTool) launchArgs(extra []string) ([]string, error) {
	args := []string{}
	if rules := grokconfig.Rules(t.contextFiles.InstructionContents()); rules != "" {
		args = append(args, "--rules", rules)
	}
	args = append(args, extra...)
	return args, nil
}
