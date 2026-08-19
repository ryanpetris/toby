// Package forgejocli provides the Forgejo CLI (forgejo-cli) tool, installed into
// the sandbox from a bundled install script.
package forgejocli

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
		http:   params.HTTP.Unwrap(),
		logger: params.Diagnostics.Logger("tools.forgejo_cli"),
	}
	svc.ScriptInstaller = &kit.ScriptInstaller{
		Base:           tools.Base{Metadata: Meta},
		Sandbox:        params.Sandbox,
		Command:        "fj",
		InstallRelPath: forgejoCLIInstallPath,
		LoadScript: func() ([]byte, error) {
			return forgejoCLIFiles.ReadFile("resources/install.sh")
		},
		ArchiveURL: svc.archiveURL,
	}
	return result{Service: svc}
}

type forgejoCLITool struct {
	*kit.ScriptInstaller
	http   *http.Client
	logger *diagnostic.Logger
}

var _ tools.Tool = (*forgejoCLITool)(nil)

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
