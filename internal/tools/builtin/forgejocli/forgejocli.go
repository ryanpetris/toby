// Package forgejocli provides the Forgejo CLI (forgejo-cli) tool, installed into
// the sandbox from a bundled install script.
package forgejocli

import (
	"context"
	"fmt"
	"net/http"
	pathpkg "path"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/kit"
	"petris.dev/toby/internal/tools/runtimepath"
)

// Name is this tool's canonical identifier.
const Name = "fj"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Forgejo CLI",
	LaunchHelp:    "Launch Forgejo CLI",
	Group:         tools.GroupVCS,
	ContextGroups: []string{tools.GroupVCS, tools.GroupSystem},
}

const forgejoCLIInstallPath = "fj/install.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &forgejoCLITool{
		Base:    tools.Base{Metadata: Meta},
		http:    params.HTTP.Unwrap(),
		logger:  params.Diagnostics.Logger("tools.forgejo_cli"),
		sandbox: params.Sandbox,
	}
	return result{Service: svc}
}

type forgejoCLITool struct {
	tools.Base
	http    *http.Client
	logger  *diagnostic.Logger
	sandbox sandbox.Service
}

var _ tools.Tool = (*forgejoCLITool)(nil)
var _ runtimeassets.Contributor = (*forgejoCLITool)(nil)

func (t *forgejoCLITool) ConfigureSandbox(ctx context.Context) error {
	return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".local", "bin"), ":")
}

func (t *forgejoCLITool) InitSandbox(ctx context.Context) error {
	return t.Install(ctx, false)
}

func (t *forgejoCLITool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := forgejoCLIFiles.ReadFile("resources/install.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, forgejoCLIInstallPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *forgejoCLITool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"fj",
		)
		if err != nil || exists {
			return err
		}
	}

	archiveURL, err := t.archiveURL(ctx)
	if err != nil {
		return err
	}
	installer, err := runtimepath.Resolve(forgejoCLIInstallPath)
	if err != nil {
		return err
	}
	_, err = t.sandbox.Exec(
		ctx,
		[]string{installer, archiveURL},
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

func (t *forgejoCLITool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"fj"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}

func (t *forgejoCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := kit.LinuxAssetArch("forgejo-cli")
	if err != nil {
		return "", err
	}
	assetName := "forgejo-cli-" + arch + "-linux.tar.gz"

	var data []struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := kit.GetJSON(ctx, t.http, t.logger, "https://codeberg.org/api/v1/repos/forgejo-contrib/forgejo-cli/releases?limit=1", "application/json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest forgejo-cli release: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("failed to resolve latest forgejo-cli release: empty release list")
	}

	for _, asset := range data[0].Assets {
		if asset.Name == assetName && strings.TrimSpace(asset.URL) != "" {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("latest forgejo-cli release does not provide %s", assetName)
}
