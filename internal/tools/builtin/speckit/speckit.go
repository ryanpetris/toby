// Package speckit provides the Spec Kit (specify-cli) tool, installed into the
// sandbox with uv.
package speckit

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"petris.dev/toby/diagnostic/exitcode"
	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"
	"petris.dev/toby/tools/helpers"
	"petris.dev/toby/tools/kit"

	"go.uber.org/fx"
)

const (
	latestReleaseURL = "https://api.github.com/repos/github/spec-kit/releases/latest"
	repositoryURL    = "https://github.com/github/spec-kit.git"
)

var Module = fx.Module("tools.speckit", fx.Provide(Provide))

// Name is this tool's canonical identifier.
const Name = "speckit"

// Meta is this tool's declarative identity. It runs after uv via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	LaunchHelp:    "Launch Spec Kit",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{uv.Name},
}

type Params struct {
	fx.In

	HTTP    *http.Client
	Sandbox sandbox.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"tools"`
}

func Provide(params Params) Result {
	svc := &speckitTool{
		Base:    tools.Base{Metadata: Meta},
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

var _ tools.Tool = (*speckitTool)(nil)

func (t *speckitTool) PrepareHost(context.Context, *tools.Options) error { return nil }

func (t *speckitTool) ConfigureSandbox(context.Context) error { return nil }

func (t *speckitTool) InitSandbox(ctx context.Context) error {
	return t.Install(ctx, false)
}

func (t *speckitTool) Install(ctx context.Context, force bool) error {
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
}

func (t *speckitTool) LaunchCommand(_ context.Context, extra []string) ([]string, error) {
	return append([]string{"specify"}, extra...), nil
}

func (t *speckitTool) latestReleaseTag(ctx context.Context) (string, error) {
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := kit.GetJSON(ctx, t.http, latestReleaseURL, "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest Spec Kit release tag: %w", err)
	}
	if strings.TrimSpace(data.TagName) == "" {
		return "", fmt.Errorf("failed to resolve latest Spec Kit release tag: missing tag_name")
	}
	return strings.TrimSpace(data.TagName), nil
}
