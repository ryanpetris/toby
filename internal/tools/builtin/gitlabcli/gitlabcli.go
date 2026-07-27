// Package gitlabcli provides the GitLab CLI (glab) tool for the sandbox.
package gitlabcli

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
const Name = "gitlab_cli"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "GitLab CLI",
	CLIName:       "glab",
	LaunchHelp:    "Launch GitLab CLI",
	Group:         tools.GroupVCS,
	ContextGroups: []string{tools.GroupVCS, tools.GroupSystem},
}

const gitlabCLIInstallPath = "gitlab_cli/install.sh"

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &gitlabCLITool{
		Base:    tools.Base{Metadata: Meta},
		http:    params.HTTP.Unwrap(),
		logger:  params.Diagnostics.Logger("tools.gitlab_cli"),
		sandbox: params.Sandbox,
	}
	return result{Service: svc}
}

type gitlabCLITool struct {
	tools.Base
	http    *http.Client
	logger  *diagnostic.Logger
	sandbox sandbox.Service
}

var _ tools.Tool = (*gitlabCLITool)(nil)
var _ runtimeassets.Contributor = (*gitlabCLITool)(nil)

func (t *gitlabCLITool) ConfigureSandbox(ctx context.Context) error {
	return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".local", "bin"), ":")
}

func (t *gitlabCLITool) InitSandbox(ctx context.Context) error {
	return t.Install(ctx, false)
}

func (t *gitlabCLITool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := gitlabCLIFiles.ReadFile("resources/install.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, gitlabCLIInstallPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *gitlabCLITool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"glab",
		)
		if err != nil || exists {
			return err
		}
	}

	archiveURL, err := t.archiveURL(ctx)
	if err != nil {
		return err
	}
	installer, err := runtimepath.Resolve(gitlabCLIInstallPath)
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

func (t *gitlabCLITool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"glab"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}

func (t *gitlabCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := kit.GoAssetArch("glab")
	if err != nil {
		return "", err
	}
	suffix := "_linux_" + arch + ".tar.gz"

	var data struct {
		Assets struct {
			Links []struct {
				Name           string `json:"name"`
				URL            string `json:"url"`
				DirectAssetURL string `json:"direct_asset_url"`
			} `json:"links"`
		} `json:"assets"`
	}
	if err := kit.GetJSON(ctx, t.http, t.logger, "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/releases/permalink/latest", "application/json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest glab release: %w", err)
	}

	for _, link := range data.Assets.Links {
		url := link.URL
		if url == "" {
			url = link.DirectAssetURL
		}
		if strings.HasSuffix(link.Name, suffix) && strings.TrimSpace(url) != "" {
			return url, nil
		}
	}
	return "", fmt.Errorf("latest glab release does not provide an asset matching *%s", suffix)
}
