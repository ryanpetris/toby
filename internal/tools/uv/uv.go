package uv

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"petris.dev/toby/container/layout"
	"strings"

	"petris.dev/toby/internal/config"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.uv", fx.Provide(Provide))

const uvInstallPath = "uv/install.sh"

//go:embed install.sh
var uvFiles embed.FS

type Result struct {
	fx.Out

	Service tool.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	Paths        config.Paths
	HTTP         *http.Client
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &uvTool{
		Base:         toolutil.Base(tool.UvToolName, "Launch UV (Python Package Manager)", tool.GroupSystem, tool.GroupVCS),
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type uvTool struct {
	tool.Base
	http         *http.Client
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *uvTool) SandboxContextSetup(ctx context.Context) error {
	return helpers.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		shared := filepath.Join(layout.Home, ".local", "share", "toby", "uv")
		for key, value := range map[string]string{
			"UV_TOOL_DIR":     filepath.Join(shared, "tools"),
			"UV_TOOL_BIN_DIR": filepath.Join(shared, "bin"),
			"UV_CACHE_DIR":    filepath.Join(shared, "cache"),
		} {
			if err := t.sandbox.SetEnvironment(ctx, key, value); err != nil {
				return err
			}
		}
		return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(shared, "bin"), ":")
	})
}

func (t *uvTool) SandboxInit(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		if err := t.Install(ctx); err != nil {
			return err
		}
		for _, key := range []string{"UV_TOOL_DIR", "UV_TOOL_BIN_DIR", "UV_CACHE_DIR"} {
			dir, _ := t.sandbox.GetEnvironment(key)
			if err := t.sandbox.MkdirOwned(ctx, dir, 0o755, control.HostUser, control.HostGroup); err != nil {
				return err
			}
		}
		return nil
	})
}

func (t *uvTool) RegisterContextFiles(ctx context.Context, _ tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := uvFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, uvInstallPath, data, 0o755)
		return err
	})
}

func (t *uvTool) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *uvTool) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *uvTool) install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, tool.ExecOptions{HideOutput: true}, "uv")
			if err != nil || exists {
				return err
			}
		}
		_, archiveURL, err := t.latestRelease(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		_, err = t.sandbox.Exec(ctx, []string{t.contextPath(uvInstallPath), archiveURL}, tool.ExecOptions{})
		return err
	})
}

func (t *uvTool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
}

func (t *uvTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"uv"}, extra...), tool.ExecOptions{Foreground: true})
	return err
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
