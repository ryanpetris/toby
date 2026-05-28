package tools

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
)

const (
	emdashAppImageURL       = "https://github.com/generalaction/emdash/releases/latest/download/emdash-x86_64.AppImage"
	grokBaseURL             = "https://x.ai/cli"
	specKitLatestReleaseURL = "https://api.github.com/repos/github/spec-kit/releases/latest"
	specKitRepositoryURL    = "https://github.com/github/spec-kit.git"
)

func init() {
	register(newEmdashTool)
	register(newGrokTool)
	register(newSpeckitTool)
}

func newEmdashTool(paths config.Paths) tool.Tool {
	return &emdashTool{Base: simpleBase(tool.EmdashToolName, "Launch Emdash", tool.GroupUI, tool.GroupAI, tool.GroupSystem, tool.GroupVCS)}
}

type emdashTool struct{ tool.Base }

func (t *emdashTool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
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
		`curl -fsSL "` + emdashAppImageURL + `" -o "$tmp_appimage";`,
		`chmod +x "$tmp_appimage";`,
		`mv -f "$tmp_appimage" "$appimage";`,
		`printf '%s\n' '#!/bin/sh' 'APPIMAGE_EXTRACT_AND_RUN=1 exec "$HOME/.local/apps/emdash.AppImage" "$@"' > "$launcher";`,
		`chmod +x "$launcher"`,
	}, " ")
	return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
}

func (t *emdashTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"emdash"}, run.Extra...), tool.ExecOptions{})
}

type grokTool struct {
	*tool.Simple
}

func newGrokTool(paths config.Paths) tool.Tool {
	return &grokTool{Simple: &tool.Simple{
		Base:           tool.Base{Metadata: tool.Metadata{Name: tool.GrokToolName, LaunchHelp: "Launch Grok", ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}}},
		RootDir:        paths.SandboxRoot,
		Home:           paths.Home,
		HostSubpath:    []string{".grok"},
		SandboxSubpath: []string{".grok"},
	}}
}

func (t *grokTool) PathEntries() []string {
	return []string{filepath.Join(t.Home, ".grok", "bin")}
}

func (t *grokTool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
	if !force {
		exists, err := tool.CommandExists(ctx, run, "grok")
		if err != nil || exists {
			return err
		}
	}
	arch, err := linuxAssetArch("grok")
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
		`version="$(curl -fsSL "` + grokBaseURL + `/stable")";`,
		`if [ -z "$version" ]; then printf "failed to resolve latest Grok version\n" >&2; exit 1; fi;`,
		`url="` + grokBaseURL + `/grok-${version}-linux-` + arch + `";`,
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
}

func (t *grokTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"grok"}, run.Extra...), tool.ExecOptions{})
}

type speckitTool struct {
	tool.Base
	http *http.Client
}

func newSpeckitTool(client *http.Client) tool.Tool {
	return &speckitTool{
		Base: tool.Base{Metadata: tool.Metadata{Name: tool.SpeckitToolName, LaunchHelp: "Launch Spec Kit", Dependencies: []string{tool.UvToolName}, ContextGroups: []string{tool.GroupAI, tool.GroupSystem, tool.GroupVCS}}},
		http: client,
	}
}

func (t *speckitTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return t.Install(ctx, run, false)
}

func (t *speckitTool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
	if !force {
		exists, err := tool.CommandExists(ctx, run, "specify")
		if err != nil || exists {
			return err
		}
	}
	tag, err := t.latestReleaseTag(ctx)
	if err != nil {
		log.Printf("%s", err)
		return exitcode.Code(1)
	}
	command := []string{"uv", "tool", "install", "specify-cli"}
	if force {
		command = append(command, "--force")
	}
	command = append(command, "--from", "git+"+specKitRepositoryURL+"@"+tag)
	return tool.RunCommand(ctx, run.Exec, command, tool.ExecOptions{})
}

func (t *speckitTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"specify"}, run.Extra...), tool.ExecOptions{})
}

func (t *speckitTool) latestReleaseTag(ctx context.Context) (string, error) {
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := getJSON(ctx, t.http, specKitLatestReleaseURL, "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest Spec Kit release tag: %w", err)
	}
	if strings.TrimSpace(data.TagName) == "" {
		return "", fmt.Errorf("failed to resolve latest Spec Kit release tag: missing tag_name")
	}
	return strings.TrimSpace(data.TagName), nil
}
