// Package githubcli provides the GitHub CLI (gh) tool for the sandbox.
package githubcli

import (
	"context"
	"embed"
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

var Module = fx.Module("tools.githubcli", fx.Provide(Provide))

const githubCLIInstallPath = "github_cli/install.sh"

//go:embed install.sh
var githubCLIFiles embed.FS

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
	svc := &githubCLITool{
		Base:         tools.Base{Metadata: tools.Metadata{Name: tools.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI", Group: tools.GroupVCS, ContextGroups: []string{tools.GroupVCS, tools.GroupSystem}}},
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type githubCLITool struct {
	tools.Base
	http         *http.Client
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

var _ tools.Tool = (*githubCLITool)(nil)

func (t *githubCLITool) InitSandbox(ctx context.Context) error {
	return t.Install(ctx, false)
}

func (t *githubCLITool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := githubCLIFiles.ReadFile("install.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, githubCLIInstallPath, data, 0o755)
	return err
}

func (t *githubCLITool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "gh")
		if err != nil || exists {
			return err
		}
	}

	archiveURL, err := t.archiveURL(ctx)
	if err != nil {
		log.Printf("%s", err)
		return exitcode.Code(1)
	}
	_, err = t.sandbox.Exec(ctx, []string{t.contextPath(githubCLIInstallPath), archiveURL}, sandbox.ExecOptions{})
	return err
}

func (t *githubCLITool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
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
	if err := kit.GetJSON(ctx, t.http, "https://api.github.com/repos/cli/cli/releases/latest", "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest gh release: %w", err)
	}

	for _, asset := range data.Assets {
		if strings.HasSuffix(asset.Name, suffix) && strings.TrimSpace(asset.URL) != "" {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("latest gh release does not provide an asset matching *%s", suffix)
}
