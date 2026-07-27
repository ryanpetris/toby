// Package uv provides the uv Python package manager tool for the sandbox.
package uv

import (
	"context"
	"fmt"
	"net/http"
	pathpkg "path"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/kit"
	"petris.dev/toby/internal/tools/runtimepath"
)

// Name is this tool's canonical identifier (the dependency name speckit
// references).
const Name = "uv"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "UV",
	LaunchHelp:    "Launch UV (Python Package Manager)",
	Group:         tools.GroupSystem,
	ContextGroups: []string{tools.GroupSystem, tools.GroupVCS},
}

const uvInstallPath = "uv/install.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &uvTool{
		Base:    tools.Base{Metadata: Meta},
		http:    params.HTTP.Unwrap(),
		logger:  params.Diagnostics.Logger("tools.uv"),
		sandbox: params.Sandbox,
	}
	return result{Service: svc}
}

type uvTool struct {
	tools.Base
	http    *http.Client
	logger  *diagnostic.Logger
	sandbox sandbox.Service
}

var _ tools.Tool = (*uvTool)(nil)
var _ runtimeassets.Contributor = (*uvTool)(nil)

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
	directories := []string{shared}
	for _, key := range []string{"UV_TOOL_DIR", "UV_TOOL_BIN_DIR", "UV_CACHE_DIR"} {
		dir, _ := t.sandbox.Environment(key)
		directories = append(directories, dir)
	}
	argv := append([]string{"mkdir", "-p", "--"}, directories...)
	if _, err := t.sandbox.Exec(
		ctx,
		argv,
		sandbox.ExecOptions{Status: "Preparing storage"},
	); err != nil {
		return err
	}

	if err := t.Install(ctx, false); err != nil {
		return err
	}
	return nil
}

func (t *uvTool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := uvFiles.ReadFile("resources/install.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, uvInstallPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *uvTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"uv",
		)
		if err != nil || exists {
			return err
		}
	}

	_, archiveURL, err := t.latestRelease(ctx)
	if err != nil {
		return err
	}
	installer, err := runtimepath.Resolve(uvInstallPath)
	if err != nil {
		return err
	}
	code, err := t.sandbox.Exec(
		ctx,
		[]string{installer, archiveURL},
		sandbox.ExecOptions{Status: "Installing"},
	)
	if err != nil {
		return err
	}
	if code != 0 {
		return exitcode.New(code, "uv install failed")
	}
	return nil
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
	if err := kit.GetJSON(ctx, t.http, t.logger, "https://api.github.com/repos/astral-sh/uv/releases/latest", "application/vnd.github+json", &data); err != nil {
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
