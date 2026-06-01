package speckit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"petris.dev/toby/internal/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

const (
	latestReleaseURL = "https://api.github.com/repos/github/spec-kit/releases/latest"
	repositoryURL    = "https://github.com/github/spec-kit.git"
)

var Module = fx.Module("tools.speckit", fx.Provide(Provide))

type Params struct {
	fx.In

	HTTP    *http.Client
	UV      tool.Tool `name:"uv"`
	Sandbox tool.SandboxService
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"speckit"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &speckitTool{
		Base:    toolutil.Base(tool.SpeckitToolName, "Launch Spec Kit", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		http:    params.HTTP,
		uv:      params.UV,
		sandbox: params.Sandbox,
	}
	return Result{Service: svc, Registry: svc}
}

type speckitTool struct {
	tool.Base
	http    *http.Client
	uv      tool.Tool
	sandbox tool.SandboxService
}

func (t *speckitTool) deps() []tool.Tool { return []tool.Tool{t.uv} }

func (t *speckitTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	return toolutil.HostInitDependencies(ctx, opts, t.uv)
}

func (t *speckitTool) SandboxContextSetup(ctx context.Context) error {
	return toolutil.SandboxContextSetupDependencies(ctx, t.uv)
}

func (t *speckitTool) SandboxInit(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		if err := toolutil.SandboxInitDependencies(ctx, t.uv); err != nil {
			return err
		}
		return t.Install(ctx)
	})
}

func (t *speckitTool) Install(ctx context.Context) error {
	if err := toolutil.InstallDependencies(ctx, t.uv); err != nil {
		return err
	}
	return t.install(ctx, false)
}

func (t *speckitTool) Upgrade(ctx context.Context) error {
	if err := toolutil.UpgradeDependencies(ctx, t.uv); err != nil {
		return err
	}
	return t.install(ctx, true)
}

func (t *speckitTool) install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, tool.ExecOptions{HideOutput: true}, "specify")
			if err != nil || exists {
				return err
			}
		}
		tag, err := t.latestReleaseTag(ctx)
		if err != nil {
			log.Printf("%s", err)
			return exitcode.Code(1)
		}
		command := []string{"uv", "tool", "install", "specify-cli"}
		if force {
			command = append(command, "--force")
		}
		command = append(command, "--from", "git+"+repositoryURL+"@"+tag)
		_, err = t.sandbox.Exec(ctx, command, tool.ExecOptions{})
		return err
	})
}

func (t *speckitTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"specify"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}

func (t *speckitTool) latestReleaseTag(ctx context.Context) (string, error) {
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := toolutil.GetJSON(ctx, t.http, latestReleaseURL, "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest Spec Kit release tag: %w", err)
	}
	if strings.TrimSpace(data.TagName) == "" {
		return "", fmt.Errorf("failed to resolve latest Spec Kit release tag: missing tag_name")
	}
	return strings.TrimSpace(data.TagName), nil
}
