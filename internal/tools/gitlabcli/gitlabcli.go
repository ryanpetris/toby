package gitlabcli

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.gitlabcli", fx.Provide(Provide))

const gitlabCLIInstallPath = "gitlab_cli/install.sh"

//go:embed install.sh
var gitlabCLIFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"gitlab_cli"`
	Registry tool.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	HTTP         *http.Client
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &gitlabCLITool{
		Base:         tool.Base{Metadata: tool.Metadata{Name: tool.GitLabCliToolName, CLIName: "glab", LaunchHelp: "Launch GitLab CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}}},
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type gitlabCLITool struct {
	tool.Base
	http         *http.Client
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *gitlabCLITool) SandboxInit(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		return t.Install(ctx)
	})
}

func (t *gitlabCLITool) RegisterContextFiles(ctx context.Context, _ tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := gitlabCLIFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, gitlabCLIInstallPath, data, 0o755)
		return err
	})
}

func (t *gitlabCLITool) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *gitlabCLITool) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *gitlabCLITool) install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, tool.ExecOptions{HideOutput: true}, "glab")
			if err != nil || exists {
				return err
			}
		}
		archiveURL, err := t.archiveURL(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		_, err = t.sandbox.Exec(ctx, []string{t.contextPath(gitlabCLIInstallPath), archiveURL}, tool.ExecOptions{})
		return err
	})
}

func (t *gitlabCLITool) contextPath(path string) string {
	return filepath.Join(t.sandbox.Paths().Context, filepath.FromSlash(path))
}

func (t *gitlabCLITool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"glab"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}

func (t *gitlabCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := toolutil.GoAssetArch("glab")
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
	if err := toolutil.GetJSON(ctx, t.http, "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/releases/permalink/latest", "application/json", &data); err != nil {
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
