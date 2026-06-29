// Package forgejocli provides the Forgejo CLI (forgejo-cli) tool, installed into
// the sandbox from a bundled install script.
package forgejocli

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
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.forgejocli", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "fj"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Forgejo CLI",
	Group:         tools.GroupVCS,
	ContextGroups: []string{tools.GroupVCS, tools.GroupSystem},
}

const forgejoCLIInstallPath = "fj/install.sh"

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
	svc := &forgejoCLITool{
		Base:         tools.Base{Metadata: Meta},
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type forgejoCLITool struct {
	tools.Base
	http         *http.Client
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

var _ tools.Tool = (*forgejoCLITool)(nil)

func (t *forgejoCLITool) ConfigureSandbox(ctx context.Context) error {
	return t.sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".local", "bin"), ":")
}

func (t *forgejoCLITool) InitSandbox(ctx context.Context) error {
	return t.Install(ctx, false)
}

func (t *forgejoCLITool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := forgejoCLIFiles.ReadFile("resources/install.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, forgejoCLIInstallPath, data, 0o755)
	return err
}

func (t *forgejoCLITool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "fj")
		if err != nil || exists {
			return err
		}
	}

	archiveURL, err := t.archiveURL(ctx)
	if err != nil {
		log.Printf("%s", err)
		return exitcode.Code(1)
	}
	_, err = t.sandbox.Exec(ctx, []string{t.contextPath(forgejoCLIInstallPath), archiveURL}, sandbox.ExecOptions{})
	return err
}

func (t *forgejoCLITool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
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
	if err := kit.GetJSON(ctx, t.http, "https://codeberg.org/api/v1/repos/forgejo-contrib/forgejo-cli/releases?limit=1", "application/json", &data); err != nil {
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
