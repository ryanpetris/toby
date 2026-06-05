package speckit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/toolutil"

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
	Sandbox sandbox.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &speckitTool{
		Base:    toolutil.DependentBase(tools.SpeckitToolName, "Launch Spec Kit", 100, []string{tools.UvToolName}, tools.GroupAI, tools.GroupSystem, tools.GroupVCS),
		http:    params.HTTP,
		sandbox: params.Sandbox,
	}
	return Result{Service: svc}
}

type speckitTool struct {
	tools.Base
	http    *http.Client
	sandbox sandbox.Service
}

func (t *speckitTool) PrepareHost(context.Context, *tools.Options) error { return nil }

func (t *speckitTool) ConfigureSandbox(context.Context) error { return nil }

func (t *speckitTool) InitSandbox(ctx context.Context) error {
	return helpers.SandboxInitOnce(ctx, t.Name(), func() error {
		return t.Install(ctx, false)
	})
}

func (t *speckitTool) Install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "specify")
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
		_, err = t.sandbox.Exec(ctx, command, sandbox.ExecOptions{})
		return err
	})
}

func (t *speckitTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"specify"}, extra...), sandbox.ExecOptions{Foreground: true})
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
