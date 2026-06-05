package grok

import (
	"context"
	"embed"
	"log"
	"path/filepath"
	"petris.dev/toby/container/layout"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	grokconfig "petris.dev/toby/internal/dirty/tools/grok/config"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/toolutil"

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
	MCPProxy     *mcpproxy.Service   `optional:"true"`
	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &grokTool{Simple: &toolutil.Simple{
		Base:           toolutil.Base(tools.GrokToolName, "Launch Grok", tools.GroupAI, tools.GroupSystem, tools.GroupVCS),
		Sandbox:        params.Sandbox,
		RootDir:        params.Paths.SandboxRoot,
		HostSubpath:    []string{".grok"},
		SandboxSubpath: []string{".grok"},
	}, config: params.Config, mcpProxy: params.MCPProxy, contextFiles: params.ContextFiles}
	svc.proxy = params.Proxy
	return Result{Service: svc}
}

type grokTool struct {
	*toolutil.Simple
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	contextFiles *contextfiles.Service
}

func (t *grokTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := grokFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		if _, err := t.contextFiles.AddFile(ctx, grokInstallPath, data, 0o755); err != nil {
			return err
		}
		controlHost, _ := t.Sandbox.GetEnvironment(control.EnvControlHost)
		return grokconfig.RegisterContextFiles(t.contextFiles.Registrar(ctx), t.contextFiles.InstructionContents(), t.config, controlHost, t.Sandbox.TobyMCPURL(), t.proxy, t.mcpProxy)
	})
}

func (t *grokTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}
	return helpers.SandboxContextSetupOnce(ctx, t.Name()+".path", func() error {
		return t.Sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".grok", "bin"), ":")
	})
}

func (t *grokTool) InitSandbox(ctx context.Context) error {
	if err := t.Simple.InitSandbox(ctx); err != nil {
		return err
	}
	return helpers.SandboxInitOnce(ctx, t.Name()+".managed-config", func() error {
		contextDir := layout.Context
		home := layout.Home
		grokHome := filepath.Join(home, ".grok")
		if err := t.Sandbox.MkdirOwned(ctx, grokHome, 0o755, control.HostUser, control.HostGroup); err != nil {
			return err
		}
		return t.Sandbox.SymlinkOwned(ctx, filepath.Join(grokHome, "managed_config.toml"), grokconfig.ConfigPath(contextDir), control.HostUser, control.HostGroup)
	})
}

func (t *grokTool) Install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.Sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "grok")
			if err != nil || exists {
				return err
			}
		}
		arch, err := toolutil.LinuxAssetArch("grok")
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		_, err = t.Sandbox.Exec(ctx, []string{t.contextPath(grokInstallPath), baseURL, arch}, sandbox.ExecOptions{})
		return err
	})
}

func (t *grokTool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
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
	if rules := grokconfig.Rules(t.contextFiles.InstructionContents()); rules != "" {
		args = append(args, "--rules", rules)
	}
	args = append(args, extra...)
	return args, nil
}
