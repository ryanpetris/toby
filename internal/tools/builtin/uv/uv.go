// Package uv provides the uv Python package manager tool for the sandbox.
package uv

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"petris.dev/toby/container/layout"
	"strings"

	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.uv", fx.Provide(Provide))

// Name is this tool's canonical identifier (the dependency name speckit
// references).
const Name = "uv"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch UV (Python Package Manager)",
	Group:         tools.GroupSystem,
	ContextGroups: []string{tools.GroupSystem, tools.GroupVCS},
}

const uvInstallPath = "uv/install.sh"

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

type Params struct {
	fx.In

	HTTP         *http.Client
	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &uvTool{
		Base:         tools.Base{Metadata: Meta},
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type uvTool struct {
	tools.Base
	http         *http.Client
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

var _ tools.Tool = (*uvTool)(nil)

func (t *uvTool) ConfigureSandbox(ctx context.Context) error {
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
	if err := t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".local", "bin"), ":"); err != nil {
		return err
	}
	return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(shared, "bin"), ":")
}

func (t *uvTool) InitSandbox(ctx context.Context) error {
	shared := filepath.Join(layout.Home, ".local", "share", "toby", "uv")
	if err := t.sandbox.MkdirOwned(ctx, shared, 0o755, control.HostUser, control.HostGroup); err != nil {
		return err
	}
	for _, key := range []string{"UV_TOOL_DIR", "UV_TOOL_BIN_DIR", "UV_CACHE_DIR"} {
		dir, _ := t.sandbox.Environment(key)
		if err := t.sandbox.MkdirOwned(ctx, dir, 0o755, control.HostUser, control.HostGroup); err != nil {
			return err
		}
	}

	if err := t.Install(ctx, false); err != nil {
		return err
	}
	return nil
}

func (t *uvTool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := uvFiles.ReadFile("resources/install.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, uvInstallPath, data, 0o755)
	return err
}

func (t *uvTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "uv")
		if err != nil || exists {
			return err
		}
	}

	_, archiveURL, err := t.latestRelease(ctx)
	if err != nil {
		log.Printf("%s", err)
		return exitcode.Code(1)
	}
	code, err := t.sandbox.Exec(ctx, []string{t.contextPath(uvInstallPath), archiveURL}, sandbox.ExecOptions{})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitcode.New(code, "uv install failed")
	}
	return nil
}

func (t *uvTool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
}

func (t *uvTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"uv"}, extra...), sandbox.ExecOptions{Foreground: true})
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
	if err := kit.GetJSON(ctx, t.http, "https://api.github.com/repos/astral-sh/uv/releases/latest", "application/vnd.github+json", &data); err != nil {
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
	triple, err := kit.RustTargetTriple("uv")
	if err != nil {
		return "", err
	}
	return "uv-" + triple + ".tar.gz", nil
}
