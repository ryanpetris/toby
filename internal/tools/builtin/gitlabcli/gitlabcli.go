// Package gitlabcli provides the GitLab CLI (glab) tool for the sandbox.
package gitlabcli

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

var Module = fx.Module("tools.gitlabcli", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "gitlab_cli"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	CLIName:       "glab",
	LaunchHelp:    "Launch GitLab CLI",
	Group:         tools.GroupVCS,
	ContextGroups: []string{tools.GroupVCS, tools.GroupSystem},
}

const gitlabCLIInstallPath = "gitlab_cli/install.sh"

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
	svc := &gitlabCLITool{
		Base:         tools.Base{Metadata: Meta},
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type gitlabCLITool struct {
	tools.Base
	http         *http.Client
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

var _ tools.Tool = (*gitlabCLITool)(nil)

func (t *gitlabCLITool) InitSandbox(ctx context.Context) error {
	return t.Install(ctx, false)
}

func (t *gitlabCLITool) RegisterContextFiles(ctx context.Context, _ tools.ContextOptions) error {
	data, err := gitlabCLIFiles.ReadFile("resources/install.sh")
	if err != nil {
		return err
	}
	_, err = t.contextFiles.AddFile(ctx, gitlabCLIInstallPath, data, 0o755)
	return err
}

func (t *gitlabCLITool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "glab")
		if err != nil || exists {
			return err
		}
	}

	archiveURL, err := t.archiveURL(ctx)
	if err != nil {
		log.Printf("%s", err)
		return exitcode.Code(1)
	}
	_, err = t.sandbox.Exec(ctx, []string{t.contextPath(gitlabCLIInstallPath), archiveURL}, sandbox.ExecOptions{})
	return err
}

func (t *gitlabCLITool) contextPath(path string) string {
	return filepath.Join(layout.Context, filepath.FromSlash(path))
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
	if err := kit.GetJSON(ctx, t.http, "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/releases/permalink/latest", "application/json", &data); err != nil {
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
