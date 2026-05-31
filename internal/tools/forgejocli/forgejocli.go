package forgejocli

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

var Module = fx.Module("tools.forgejocli", fx.Provide(Provide))

const forgejoCLIInstallPath = "fj/install"

//go:embed install
var forgejoCLIFiles embed.FS

type Result struct {
	fx.Out

	Service  tool.Tool `name:"fj"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(client *http.Client) Result {
	svc := &forgejoCLITool{
		Base: toolutil.Base(tool.ForgejoCliToolName, "Launch Forgejo CLI", tool.GroupSystem, tool.GroupVCS),
		http: client,
	}
	return Result{Service: svc, Registry: svc}
}

type forgejoCLITool struct {
	tool.Base
	http *http.Client
}

func (t *forgejoCLITool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return tool.SandboxInitOnce(run, t.Name(), func() error {
		return t.Install(ctx, run)
	})
}

func (t *forgejoCLITool) RegisterContextFiles(_ context.Context, run *tool.RunContext) error {
	return tool.RegisterContextFilesOnce(run, t.Name(), func() error {
		if run == nil || run.ContextFiles == nil {
			return fmt.Errorf("context files session is not configured")
		}
		data, err := forgejoCLIFiles.ReadFile("install")
		if err != nil {
			return err
		}
		return run.ContextFiles.AddBytes(forgejoCLIInstallPath, data, 0o500)
	})
}

func (t *forgejoCLITool) Install(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, false)
}

func (t *forgejoCLITool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	return t.install(ctx, run, true)
}

func (t *forgejoCLITool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
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
		path, err := forgejoCLIInstallLaunchPath(run)
		if err != nil {
			return err
		}
		return tool.RunCommand(ctx, run.Exec, []string{path, archiveURL}, tool.ExecOptions{})
	})
}

func forgejoCLIInstallLaunchPath(run *tool.RunContext) (string, error) {
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
	return filepath.Join(contextDir, filepath.FromSlash(forgejoCLIInstallPath)), nil
}

func (t *forgejoCLITool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"fj"}, run.Extra...), tool.ExecOptions{})
}

func (t *forgejoCLITool) archiveURL(ctx context.Context) (string, error) {
	arch, err := toolutil.LinuxAssetArch("forgejo-cli")
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
	if err := toolutil.GetJSON(ctx, t.http, "https://codeberg.org/api/v1/repos/forgejo-contrib/forgejo-cli/releases?limit=1", "application/json", &data); err != nil {
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
