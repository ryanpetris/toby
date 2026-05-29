package githubcli

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/shellquote"
	"petris.dev/toby/internal/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.githubcli", fx.Provide(Provide))

type Result struct {
	fx.Out

	Service  tool.Tool `name:"github_cli"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(client *http.Client) Result {
	svc := &githubCLITool{
		Base: tool.Base{Metadata: tool.Metadata{Name: tool.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}}},
		http: client,
	}
	return Result{Service: svc, Registry: svc}
}

type githubCLITool struct {
	tool.Base
	http *http.Client
}

func (t *githubCLITool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return tool.SandboxInitOnce(run, t.Name(), func() error {
		return t.Install(ctx, run)
	})
}

func (t *githubCLITool) Install(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, false)
}

func (t *githubCLITool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, true)
}

func (t *githubCLITool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "gh")
			if err != nil || exists {
				return err
			}
		}
		archiveURL, err := t.archiveURL(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		script := strings.Join([]string{
			"set -euo pipefail;",
			`tmp="$(mktemp -d)";`,
			`trap 'rm -rf "$tmp"' EXIT;`,
			`archive="$tmp/gh.tar.gz";`,
			"curl -fsSL " + shellquote.Quote(archiveURL) + ` -o "$archive";`,
			`tar -xzf "$archive" -C "$tmp";`,
			`install -m 0755 "$tmp"/*/bin/gh "$HOME/.local/bin/gh"`,
		}, " ")
		return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
	})
}

func (t *githubCLITool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"gh"}, run.Extra...), tool.ExecOptions{})
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
