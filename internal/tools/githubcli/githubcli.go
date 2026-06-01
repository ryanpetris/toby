package githubcli

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

var Module = fx.Module("tools.githubcli", fx.Provide(Provide))

const githubCLIInstallPath = "github_cli/install.sh"

//go:embed install.sh
var githubCLIFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"github_cli"`
	Registry tool.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	HTTP         *http.Client
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &githubCLITool{
		Base:         tool.Base{Metadata: tool.Metadata{Name: tool.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}}},
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type githubCLITool struct {
	tool.Base
	http         *http.Client
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *githubCLITool) SandboxInit(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		return t.Install(ctx)
	})
}

func (t *githubCLITool) RegisterContextFiles(ctx context.Context, _ tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := githubCLIFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, githubCLIInstallPath, data, 0o500)
		return err
	})
}

func (t *githubCLITool) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *githubCLITool) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *githubCLITool) install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, tool.ExecOptions{HideOutput: true}, "gh")
			if err != nil || exists {
				return err
			}
		}
		archiveURL, err := t.archiveURL(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		_, err = t.sandbox.Exec(ctx, []string{t.contextPath(githubCLIInstallPath), archiveURL}, tool.ExecOptions{})
		return err
	})
}

func (t *githubCLITool) contextPath(path string) string {
	return filepath.Join(t.sandbox.Paths().Context, filepath.FromSlash(path))
}

func (t *githubCLITool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"gh"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}

func (t *githubCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := toolutil.GoAssetArch("gh")
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
	if err := toolutil.GetJSON(ctx, t.http, "https://api.github.com/repos/cli/cli/releases/latest", "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest gh release: %w", err)
	}
	for _, asset := range data.Assets {
		if strings.HasSuffix(asset.Name, suffix) && strings.TrimSpace(asset.URL) != "" {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("latest gh release does not provide an asset matching *%s", suffix)
}
