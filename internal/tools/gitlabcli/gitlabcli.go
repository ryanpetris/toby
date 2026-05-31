package gitlabcli

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.gitlabcli", fx.Provide(Provide))

const gitlabCLIInstallPath = "gitlab_cli/install"

//go:embed install
var gitlabCLIFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"gitlab_cli"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(client *http.Client) Result {
	svc := &gitlabCLITool{
		Base: tool.Base{Metadata: tool.Metadata{Name: tool.GitLabCliToolName, CLIName: "glab", LaunchHelp: "Launch GitLab CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}}},
		http: client,
	}
	return Result{Service: svc, Registry: svc}
}

type gitlabCLITool struct {
	tool.Base
	http *http.Client
}

func (t *gitlabCLITool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return tool.SandboxInitOnce(run, t.Name(), func() error {
		return t.Install(ctx, run)
	})
}

func (t *gitlabCLITool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	if run == nil || run.ContextFiles == nil {
		return fmt.Errorf("context files session is not configured")
	}
	data, err := gitlabCLIFiles.ReadFile("install")
	if err != nil {
		return err
	}
	return run.ContextFiles.AddBytes(gitlabCLIInstallPath, data, 0o500)
}

func (t *gitlabCLITool) Install(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, false)
}

func (t *gitlabCLITool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, true)
}

func (t *gitlabCLITool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "glab")
			if err != nil || exists {
				return err
			}
		}
		archiveURL, err := t.archiveURL(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		path, err := gitlabCLIInstallLaunchPath(run)
		if err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{path, archiveURL}, tool.ExecOptions{})
	})
}

func gitlabCLIInstallLaunchPath(run *tool.RunContext) (string, error) {
	contextDir := ""
	if run != nil {
		if run.ContextFiles != nil {
			contextDir = run.ContextFiles.ContextDir()
		}
		if contextDir == "" && run.Sandbox != nil {
			contextDir = run.Sandbox.TobyContextDir()
		}
	}
	if contextDir == "" {
		return "", fmt.Errorf("sandbox context directory is not configured")
	}
	return filepath.Join(contextDir, filepath.FromSlash(gitlabCLIInstallPath)), nil
}

func (t *gitlabCLITool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"glab"}, run.Extra...), tool.ExecOptions{})
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
