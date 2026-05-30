package uv

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.uv", fx.Provide(Provide))

const uvInstallPath = "uv/install"

//go:embed install
var uvFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"uv"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(_ config.Paths, client *http.Client) Result {
	svc := &uvTool{
		Base: toolutil.Base(tool.UvToolName, "Launch UV (Python Package Manager)", tool.GroupSystem, tool.GroupVCS),
		http: client,
	}
	return Result{Service: svc, Registry: svc}
}

type uvTool struct {
	tool.Base
	http *http.Client
}

func (t *uvTool) PathEntries() []tool.PathTarget {
	return []tool.PathTarget{tool.HomeTarget(".local", "share", "toby", "uv", "bin")}
}

func (t *uvTool) SandboxContextSetup(ctx *tool.RunContext) error {
	return tool.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		shared := filepath.Join(ctx.Sandbox.HomeDir(), ".local", "share", "toby", "uv")
		ctx.Env["UV_TOOL_DIR"] = filepath.Join(shared, "tools")
		ctx.Env["UV_TOOL_BIN_DIR"] = filepath.Join(shared, "bin")
		ctx.Env["UV_CACHE_DIR"] = filepath.Join(shared, "cache")
		return nil
	})
}

func (t *uvTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return tool.SandboxInitOnce(run, t.Name(), func() error {
		if err := t.Install(ctx, run); err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{"mkdir", "-p", run.Env["UV_TOOL_DIR"], run.Env["UV_TOOL_BIN_DIR"], run.Env["UV_CACHE_DIR"]}, tool.ExecOptions{})
	})
}

func (t *uvTool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	data, err := uvFiles.ReadFile("install")
	if err != nil {
		return err
	}
	return run.ContextFiles.AddBytes(uvInstallPath, data, 0o500)
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
		path, err := uvInstallLaunchPath(run)
		if err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{path, archiveURL}, tool.ExecOptions{})
	})
}

func uvInstallLaunchPath(run *tool.RunContext) (string, error) {
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
	return filepath.Join(contextDir, filepath.FromSlash(uvInstallPath)), nil
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
