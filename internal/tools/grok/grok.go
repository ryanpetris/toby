package grok

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/grokconfig"
	"petris.dev/toby/internal/tobyconfig"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const baseURL = "https://x.ai/cli"

var Module = fx.Module("tools.grok", fx.Provide(Provide))

type Params struct {
	fx.In

	Paths  config.Paths
	Config *tobyconfig.Service `optional:"true"`
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
	return Result{Service: svc, Registry: svc}
}

type grokTool struct {
	*tool.Simple
	config *tobyconfig.Service
}

func (t *grokTool) PathEntries() []tool.PathTarget {
	return []tool.PathTarget{tool.HomeTarget(".grok", "bin")}
}

func (t *grokTool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	return grokconfig.RegisterContextFiles(run.ContextFiles, run.ContextFiles.InstructionContents(), t.config)
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
		script := strings.Join([]string{
			"set -euo pipefail;",
			`if ! command -v curl >/dev/null 2>&1; then printf "curl is required to install grok\n" >&2; exit 127; fi;`,
			`grok_dir="$HOME/.grok";`,
			`downloads_dir="$grok_dir/downloads";`,
			`bin_dir="$grok_dir/bin";`,
			`mkdir -p "$downloads_dir" "$bin_dir";`,
			`version="$(curl -fsSL "` + baseURL + `/stable")";`,
			`if [ -z "$version" ]; then printf "failed to resolve latest Grok version\n" >&2; exit 1; fi;`,
			`url="` + baseURL + `/grok-${version}-linux-` + arch + `";`,
			`binary="$downloads_dir/grok-linux-` + arch + `";`,
			`tmp_binary="$binary.tmp";`,
			`trap 'rm -f "$tmp_binary"' EXIT;`,
			`curl -fsSL "$url" -o "$tmp_binary";`,
			`chmod +x "$tmp_binary";`,
			`mv -f "$tmp_binary" "$binary";`,
			`ln -sf "../downloads/$(basename "$binary")" "$bin_dir/grok";`,
			`ln -sf "../downloads/$(basename "$binary")" "$bin_dir/agent"`,
		}, " ")
		return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
	})
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
