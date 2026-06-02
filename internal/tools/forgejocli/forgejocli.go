package forgejocli

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

var Module = fx.Module("tools.forgejocli", fx.Provide(Provide))

const forgejoCLIInstallPath = "fj/install.sh"

//go:embed install.sh
var forgejoCLIFiles embed.FS

type Result struct {
	fx.Out

	Service tool.Tool `group:"toby.tools"`
}

type Params struct {
	fx.In

	HTTP         *http.Client
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

func Provide(params Params) Result {
	svc := &forgejoCLITool{
		Base:         toolutil.Base(tool.ForgejoCliToolName, "Launch Forgejo CLI", tool.GroupSystem, tool.GroupVCS),
		http:         params.HTTP,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type forgejoCLITool struct {
	tool.Base
	http         *http.Client
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *forgejoCLITool) SandboxInit(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		return t.Install(ctx)
	})
}

func (t *forgejoCLITool) RegisterContextFiles(ctx context.Context, _ tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		data, err := forgejoCLIFiles.ReadFile("install.sh")
		if err != nil {
			return err
		}
		_, err = t.contextFiles.AddFile(ctx, forgejoCLIInstallPath, data, 0o755)
		return err
	})
}

func (t *forgejoCLITool) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *forgejoCLITool) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *forgejoCLITool) install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, tool.ExecOptions{HideOutput: true}, "fj")
			if err != nil || exists {
				return err
			}
		}
		archiveURL, err := t.archiveURL(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		_, err = t.sandbox.Exec(ctx, []string{t.contextPath(forgejoCLIInstallPath), archiveURL}, tool.ExecOptions{})
		return err
	})
}

func (t *forgejoCLITool) contextPath(path string) string {
	return filepath.Join(t.sandbox.Paths().Context, filepath.FromSlash(path))
}

func (t *forgejoCLITool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"fj"}, extra...), tool.ExecOptions{Foreground: true})
	return err
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
