package speckit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"petris.dev/toby/internal/exitcode"
	"petris.dev/toby/internal/tool"
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

	HTTP *http.Client
	UV   tool.Tool `name:"uv"`
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"speckit"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &speckitTool{
		Base: toolutil.Base(tool.SpeckitToolName, "Launch Spec Kit", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		http: params.HTTP,
		uv:   params.UV,
	}
	return Result{Service: svc, Registry: svc}
}

type speckitTool struct {
	tool.Base
	http *http.Client
	uv   tool.Tool
}

func (t *speckitTool) deps() []tool.Tool { return []tool.Tool{t.uv} }

func (t *speckitTool) Binds() []tool.Bind {
	return toolutil.Binds(t.deps(), nil)
}

func (t *speckitTool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), nil)
}

func (t *speckitTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	return toolutil.HostInitDependencies(ctx, opts, t.uv)
}

func (t *speckitTool) SandboxContextSetup(ctx *tool.RunContext) error {
	return toolutil.SandboxContextSetupDependencies(ctx, t.uv)
}

func (t *speckitTool) SandboxInit(ctx context.Context, run *tool.RunContext) error {
	return tool.SandboxInitOnce(run, t.Name(), func() error {
		if err := toolutil.SandboxInitDependencies(ctx, run, t.uv); err != nil {
			return err
		}
		return t.Install(ctx, run)
	})
}

func (t *speckitTool) Install(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.InstallDependencies(ctx, run, t.uv); err != nil {
		return err
	}
	return t.install(ctx, run, false)
}

func (t *speckitTool) Upgrade(ctx context.Context, run *tool.RunContext) error {
	if err := toolutil.UpgradeDependencies(ctx, run, t.uv); err != nil {
		return err
	}
	return t.install(ctx, run, true)
}

func (t *speckitTool) install(ctx context.Context, run *tool.RunContext, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
	}
	return once(run, t.Name(), func() error {
		if !force {
			exists, err := tool.CommandExists(ctx, run, "specify")
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
		return tool.RunCommand(ctx, run.Exec, command, tool.ExecOptions{})
	})
}

func (t *speckitTool) Launch(ctx context.Context, run *tool.RunContext) error {
	return tool.RunCommand(ctx, run.Launch, append([]string{"specify"}, run.Extra...), tool.ExecOptions{})
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
