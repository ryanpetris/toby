package emdash

import (
	"context"
	"strings"

	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const appImageURL = "https://github.com/generalaction/emdash/releases/latest/download/emdash-x86_64.AppImage"

var Module = fx.Module("tools.emdash", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service  tool.Tool `name:"emdash"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide() Result {
	svc := &emdashTool{Base: toolutil.Base(tool.EmdashToolName, "Launch Emdash", tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS)}
	return Result{Service: svc, Registry: svc}
}

type emdashTool struct{ tool.Base }

func (t *emdashTool) Install(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, false)
}

func (t *emdashTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, true)
}

func (t *emdashTool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "emdash")
			if err != nil || exists {
				return err
			}
		}
		script := strings.Join([]string{
			"set -euo pipefail;",
			`if ! command -v curl >/dev/null 2>&1; then printf "curl is required to install emdash\n" >&2; exit 127; fi;`,
			`apps_dir="$HOME/.local/apps";`,
			`appimage="$apps_dir/emdash.AppImage";`,
			`tmp_appimage="$appimage.tmp";`,
			`bin_dir="$HOME/.local/bin";`,
			`launcher="$bin_dir/emdash";`,
			`mkdir -p "$apps_dir" "$bin_dir";`,
			`rm -rf "$apps_dir/emdash" "$tmp_appimage";`,
			`cleanup() { rm -f "$tmp_appimage"; };`,
			`trap cleanup EXIT;`,
			`curl -fsSL "` + appImageURL + `" -o "$tmp_appimage";`,
			`chmod +x "$tmp_appimage";`,
			`mv -f "$tmp_appimage" "$appimage";`,
			`printf '%s\n' '#!/bin/sh' 'APPIMAGE_EXTRACT_AND_RUN=1 exec "$HOME/.local/apps/emdash.AppImage" "$@"' > "$launcher";`,
			`chmod +x "$launcher"`,
		}, " ")
		return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
	})
}

func (t *emdashTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"emdash"}, run.Extra...), tool.ExecOptions{})
}
