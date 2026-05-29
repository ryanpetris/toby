package grok

import (
	"context"
	"log"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const baseURL = "https://x.ai/cli"

var Module = fx.Module("tools.grok", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service  tool.Tool `name:"grok"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(paths config.Paths) Result {
	svc := &grokTool{Simple: &tool.Simple{
		Base:           toolutil.Base(tool.GrokToolName, "Launch Grok", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		RootDir:        paths.SandboxRoot,
		Home:           paths.Home,
		HostSubpath:    []string{".grok"},
		SandboxSubpath: []string{".grok"},
	}}
	return Result{Service: svc, Registry: svc}
}

type grokTool struct {
	*tool.Simple
}

func (t *grokTool) PathEntries() []string {
	return []string{filepath.Join(t.Home, ".grok", "bin")}
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
	return tool.RunCommand(ctx, run.Launch, append([]string{"grok"}, run.Extra...), tool.ExecOptions{})
}
