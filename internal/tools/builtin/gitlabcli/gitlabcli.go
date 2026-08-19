// Package gitlabcli provides the GitLab CLI (glab) tool for the sandbox.
package gitlabcli

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/kit"
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
		http:   params.HTTP.Unwrap(),
		logger: params.Diagnostics.Logger("tools.gitlab_cli"),
	}
	svc.ScriptInstaller = &kit.ScriptInstaller{
		Base:           tools.Base{Metadata: Meta},
		Sandbox:        params.Sandbox,
		Command:        "glab",
		InstallRelPath: gitlabCLIInstallPath,
		LoadScript: func() ([]byte, error) {
			return gitlabCLIFiles.ReadFile("resources/install.sh")
		},
		ArchiveURL: svc.archiveURL,
	}
	return result{Service: svc}
}

type gitlabCLITool struct {
	*kit.ScriptInstaller
	http   *http.Client
	logger *diagnostic.Logger
}

var _ tools.Tool = (*gitlabCLITool)(nil)

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
