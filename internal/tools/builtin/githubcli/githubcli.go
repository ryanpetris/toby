// Package githubcli provides the GitHub CLI (gh) tool for the sandbox.
package githubcli

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
const Name = "github_cli"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "GitHub CLI",
	CLIName:       "gh",
	LaunchHelp:    "Launch GitHub CLI",
	Group:         tools.GroupVCS,
	ContextGroups: []string{tools.GroupVCS, tools.GroupSystem},
}

const githubCLIInstallPath = "github_cli/install.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &githubCLITool{
		Base:    tools.Base{Metadata: Meta},
		http:    params.HTTP.Unwrap(),
		logger:  params.Diagnostics.Logger("tools.github_cli"),
		sandbox: params.Sandbox,
	}
	return result{Service: svc}
}

type githubCLITool struct {
	tools.Base
	http    *http.Client
	logger  *diagnostic.Logger
	sandbox sandbox.Service
}

var _ tools.Tool = (*githubCLITool)(nil)
var _ runtimeassets.Contributor = (*githubCLITool)(nil)

func (t *githubCLITool) ConfigureSandbox(ctx context.Context) error {
	return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".local", "bin"), ":")
}

func (t *githubCLITool) InitSandbox(ctx context.Context) error {
	return t.Install(ctx, false)
}

func (t *githubCLITool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := githubCLIFiles.ReadFile("resources/install.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, githubCLIInstallPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *githubCLITool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"gh",
		)
		if err != nil || exists {
			return err
		}
	}

	archiveURL, err := t.archiveURL(ctx)
	if err != nil {
		return err
	}
	installer, err := runtimepath.Resolve(githubCLIInstallPath)
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

func (t *githubCLITool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"gh"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}

func (t *githubCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := kit.GoAssetArch("gh")
	if err != nil {
		return "", err
	}
	suffix := "_linux_" + arch + ".tar.gz"

	var data struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := kit.GetJSON(ctx, t.http, t.logger, "https://api.github.com/repos/cli/cli/releases/latest", "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest gh release: %w", err)
	}

	for _, asset := range data.Assets {
		if strings.HasSuffix(asset.Name, suffix) && strings.TrimSpace(asset.URL) != "" {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("latest gh release does not provide an asset matching *%s", suffix)
}
