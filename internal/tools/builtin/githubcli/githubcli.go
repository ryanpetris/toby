// Package githubcli provides the GitHub CLI (gh) tool for the sandbox.
package githubcli

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
		http:   params.HTTP.Unwrap(),
		logger: params.Diagnostics.Logger("tools.github_cli"),
	}
	svc.ScriptInstaller = &kit.ScriptInstaller{
		Base:           tools.Base{Metadata: Meta},
		Sandbox:        params.Sandbox,
		Command:        "gh",
		InstallRelPath: githubCLIInstallPath,
		LoadScript: func() ([]byte, error) {
			return githubCLIFiles.ReadFile("resources/install.sh")
		},
		ArchiveURL: svc.archiveURL,
	}
	return result{Service: svc}
}

type githubCLITool struct {
	*kit.ScriptInstaller
	http   *http.Client
	logger *diagnostic.Logger
}

var _ tools.Tool = (*githubCLITool)(nil)

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
