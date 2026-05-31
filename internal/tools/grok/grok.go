package grok

import (
	"context"
	"embed"
	"fmt"
	"log"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/diagnostic/exitcode"
	grokconfig "petris.dev/toby/internal/tools/grok/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const baseURL = "https://x.ai/cli"

var Module = fx.Module("tools.grok", fx.Provide(Provide))

const grokInstallPath = "grok/install"

//go:embed install
var grokFiles embed.FS

type Params struct {
	fx.In

	Paths  config.Paths
	Config *tobyconfig.Service `optional:"true"`
	Proxy  *httpproxy.Service  `optional:"true"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"grok"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &grokTool{Simple: &tool.Simple{
		Base:           toolutil.Base(tool.GrokToolName, "Launch Grok", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		RootDir:        params.Paths.SandboxRoot,
		HostSubpath:    []string{".grok"},
		SandboxSubpath: []string{".grok"},
	}, config: params.Config}
	svc.proxy = params.Proxy
	return Result{Service: svc, Registry: svc}
}

type grokTool struct {
	*tool.Simple
	config *tobyconfig.Service
	proxy  *httpproxy.Service
}

func (t *grokTool) PathEntries() []tool.PathTarget {
	return []tool.PathTarget{tool.HomeTarget(".grok", "bin")}
}

func (t *grokTool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	return tool.RegisterContextFilesOnce(run, t.Name(), func() error {
		if run == nil || run.ContextFiles == nil {
			return fmt.Errorf("context files session is not configured")
		}
		data, err := grokFiles.ReadFile("install")
		if err != nil {
			return err
		}
		if err := run.ContextFiles.AddBytes(grokInstallPath, data, 0o500); err != nil {
			return err
		}
		return grokconfig.RegisterContextFiles(run.ContextFiles, run.ContextFiles.InstructionContents(), t.config, run.Env[control.EnvControlHost], run.TobyMCPURL, t.proxy)
	})
}

func (t *grokTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	if err := t.Simple.SandboxInit(ctx, run); err != nil {
		return err
	}
	return tool.SandboxInitOnce(run, t.Name()+".managed-config", func() error {
		contextDir := ""
		home := ""
		if run != nil {
			if run.ContextFiles != nil {
				contextDir = run.ContextFiles.ContextDir()
			}
			if contextDir == "" && run.Sandbox != nil {
				contextDir = run.Sandbox.TobyContextDir()
			}
			if run.Sandbox != nil {
				home = run.Sandbox.HomeDir()
			}
			if home == "" {
				home = run.Env["HOME"]
			}
		}
		if contextDir == "" {
			return fmt.Errorf("context files session is not configured")
		}
		if home == "" {
			return fmt.Errorf("sandbox home is not configured")
		}
		if run == nil || run.Exec == nil {
			return fmt.Errorf("sandbox executor is not configured")
		}
		grokHome := filepath.Join(home, ".grok")
		if err := tool.RunCommand(ctx, run.Exec, []string{"mkdir", "-p", grokHome}, tool.ExecOptions{}); err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{"ln", "-sfn", grokconfig.ConfigPath(contextDir), filepath.Join(grokHome, "managed_config.toml")}, tool.ExecOptions{})
	})
}

func (t *grokTool) Install(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, false)
}

func (t *grokTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, true)
}

func (t *grokTool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "grok")
			if err != nil || exists {
				return err
			}
		}
		arch, err := toolutil.LinuxAssetArch("grok")
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		path, err := grokInstallLaunchPath(run)
		if err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{path, baseURL, arch}, tool.ExecOptions{})
	})
}

func grokInstallLaunchPath(run *tool.RunContext) (string, error) {
	contextDir := ""
	if run != nil {
		if run.ContextFiles != nil {
			contextDir = run.ContextFiles.ContextDir()
		}
		if contextDir == "" && run.Sandbox != nil {
			contextDir = run.Sandbox.TobyContextDir()
		}
	}
	if contextDir == "" {
		return "", fmt.Errorf("sandbox context directory is not configured")
	}
	return filepath.Join(contextDir, filepath.FromSlash(grokInstallPath)), nil
}

func (t *grokTool) Launch(ctx context.Context, run *tool.RunContext) error {
	args, err := launchArgs(run)
	if err != nil {
		return err
	}
	return tool.RunCommand(ctx, run.Launch, append([]string{"grok"}, args...), tool.ExecOptions{})
}

func launchArgs(run *tool.RunContext) ([]string, error) {
	extra := []string(nil)
	var instructions [][]byte
	if run != nil {
		extra = run.Extra
		if run.ContextFiles != nil {
			instructions = run.ContextFiles.InstructionContents()
		}
	}
	args := []string{}
	if rules := grokconfig.Rules(instructions); rules != "" {
		args = append(args, "--rules", rules)
	}
	args = append(args, extra...)
	return args, nil
}
