package tools

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/shellquote"
	"petris.dev/toby/internal/tool"
)

func init() {
	register(newGitHubCLITool)
	register(newGitLabCLITool)
	register(newForgejoCLITool)
}

type githubCLITool struct {
	tool.Base
	http *http.Client
}

func newGitHubCLITool(client *http.Client) tool.Tool {
	return &githubCLITool{
		Base: tool.Base{Metadata: tool.Metadata{Name: tool.GitHubCliToolName, CLIName: "gh", LaunchHelp: "Launch GitHub CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}}},
		http: client,
	}
}

func (t *githubCLITool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return t.Install(ctx, run, false)
}

func (t *githubCLITool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
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
}

func (t *githubCLITool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"gh"}, run.Extra...), tool.ExecOptions{})
}

func (t *githubCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := goAssetArch("gh")
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
	if err := getJSON(ctx, t.http, "https://api.github.com/repos/cli/cli/releases/latest", "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest gh release: %w", err)
	}
	for _, asset := range data.Assets {
		if strings.HasSuffix(asset.Name, suffix) && strings.TrimSpace(asset.URL) != "" {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("latest gh release does not provide an asset matching *%s", suffix)
}

type gitlabCLITool struct {
	tool.Base
	http *http.Client
}

func newGitLabCLITool(client *http.Client) tool.Tool {
	return &gitlabCLITool{
		Base: tool.Base{Metadata: tool.Metadata{Name: tool.GitLabCliToolName, CLIName: "glab", LaunchHelp: "Launch GitLab CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}}},
		http: client,
	}
}

func (t *gitlabCLITool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return t.Install(ctx, run, false)
}

func (t *gitlabCLITool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
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
	script := strings.Join([]string{
		"set -euo pipefail;",
		`tmp="$(mktemp -d)";`,
		`trap 'rm -rf "$tmp"' EXIT;`,
		`archive="$tmp/glab.tar.gz";`,
		"curl -fsSL " + shellquote.Quote(archiveURL) + ` -o "$archive";`,
		`tar -xzf "$archive" -C "$tmp";`,
		`install -m 0755 "$tmp/bin/glab" "$HOME/.local/bin/glab"`,
	}, " ")
	return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
}

func (t *gitlabCLITool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"glab"}, run.Extra...), tool.ExecOptions{})
}

func (t *gitlabCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := goAssetArch("glab")
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
	if err := getJSON(ctx, t.http, "https://gitlab.com/api/v4/projects/gitlab-org%2Fcli/releases/permalink/latest", "application/json", &data); err != nil {
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

type forgejoCLITool struct {
	tool.Base
	http *http.Client
}

func newForgejoCLITool(client *http.Client) tool.Tool {
	return &forgejoCLITool{
		Base: tool.Base{Metadata: tool.Metadata{Name: tool.ForgejoCliToolName, LaunchHelp: "Launch Forgejo CLI", ContextGroups: []string{tool.GroupSystem, tool.GroupVCS}}},
		http: client,
	}
}

func (t *forgejoCLITool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return t.Install(ctx, run, false)
}

func (t *forgejoCLITool) Install(ctx context.Context, run *tool.RunContext, force bool) error {
	if !force {
		exists, err := tool.CommandExists(ctx, run, "fj")
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
		`archive="$tmp/fj.tar.gz";`,
		"curl -fsSL " + shellquote.Quote(archiveURL) + ` -o "$archive";`,
		`tar -xzf "$archive" -C "$tmp";`,
		`install -m 0755 "$tmp/fj" "$HOME/.local/bin/fj"`,
	}, " ")
	return tool.RunCommand(ctx, run.Exec, []string{"bash", "-lc", script}, tool.ExecOptions{})
}

func (t *forgejoCLITool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"fj"}, run.Extra...), tool.ExecOptions{})
}

func (t *forgejoCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := linuxAssetArch("forgejo-cli")
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
	if err := getJSON(ctx, t.http, "https://codeberg.org/api/v1/repos/forgejo-contrib/forgejo-cli/releases?limit=1", "application/json", &data); err != nil {
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
