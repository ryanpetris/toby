package uv

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/shellquote"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.uv", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service  tool.Tool `name:"uv"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(paths config.Paths, client *http.Client) Result {
	svc := &uvTool{
		Base:  toolutil.Base(tool.UvToolName, "Launch UV (Python Package Manager)", tool.GroupSystem, tool.GroupVCS),
		paths: paths,
		http:  client,
	}
	return Result{Service: svc, Registry: svc}
}

type uvTool struct {
	tool.Base
	paths config.Paths
	http  *http.Client
}

func (t *uvTool) sharedDir() string {
	return filepath.Join(t.paths.Home, ".local", "share", "toby", "uv")
}

func (t *uvTool) toolDir() string { return filepath.Join(t.sharedDir(), "tools") }

func (t *uvTool) binDir() string { return filepath.Join(t.sharedDir(), "bin") }

func (t *uvTool) cacheDir() string { return filepath.Join(t.sharedDir(), "cache") }

func (t *uvTool) PathEntries() []string { return []string{t.binDir()} }

func (t *uvTool) SandboxContextSetup(ctx *tool.RunContext) error {
	return tool.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		ctx.Env["UV_TOOL_DIR"] = t.toolDir()
		ctx.Env["UV_TOOL_BIN_DIR"] = t.binDir()
		ctx.Env["UV_CACHE_DIR"] = t.cacheDir()
		return nil
	})
}

func (t *uvTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return tool.SandboxInitOnce(run, t.Name(), func() error {
		if err := t.Install(ctx, run); err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{"mkdir", "-p", t.toolDir(), t.binDir(), t.cacheDir()}, tool.ExecOptions{})
	})
}

func (t *uvTool) Install(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, false)
}

func (t *uvTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, true)
}

func (t *uvTool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "uv")
			if err != nil || exists {
				return err
			}
		}
		_, archiveURL, err := t.latestRelease(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		script := strings.Join([]string{
			"set -euo pipefail;",
			`tmp="$(mktemp -d)";`,
			`trap 'rm -rf "$tmp"' EXIT;`,
			`archive="$tmp/uv.tar.gz";`,
			"curl -fsSL " + shellquote.Quote(archiveURL) + ` -o "$archive";`,
			`tar -xzf "$archive" -C "$tmp";`,
			`install -m 0755 "$tmp"/*/uv "$HOME/.local/bin/uv";`,
			`install -m 0755 "$tmp"/*/uvx "$HOME/.local/bin/uvx"`,
		}, " ")
		return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
	})
}

func (t *uvTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"uv"}, run.Extra...), tool.ExecOptions{})
}

func (t *uvTool) latestRelease(ctx context.Context) (string, string, error) {
	assetName, err := t.assetName()
	if err != nil {
		return "", "", err
	}
	var data struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := toolutil.GetJSON(ctx, t.http, "https://api.github.com/repos/astral-sh/uv/releases/latest", "application/vnd.github+json", &data); err != nil {
		return "", "", fmt.Errorf("failed to fetch latest uv release: %w", err)
	}
	if strings.TrimSpace(data.TagName) == "" {
		return "", "", fmt.Errorf("failed to resolve latest uv release: missing tag_name")
	}
	for _, asset := range data.Assets {
		if asset.Name == assetName && strings.TrimSpace(asset.URL) != "" {
			return strings.TrimSpace(data.TagName), asset.URL, nil
		}
	}
	return "", "", fmt.Errorf("latest uv release does not provide %s", assetName)
}

func (t *uvTool) assetName() (string, error) {
	triple, err := toolutil.RustTargetTriple("uv")
	if err != nil {
		return "", err
	}
	return "uv-" + triple + ".tar.gz", nil
}
