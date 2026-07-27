// Package speckit provides the Spec Kit (specify-cli) tool, installed into the
// sandbox with uv.
package speckit

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/builtin/uv"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/kit"
)

const (
	latestReleaseURL = "https://api.github.com/repos/github/spec-kit/releases/latest"
	repositoryURL    = "https://github.com/github/spec-kit.git"
)

// Name is this tool's canonical identifier.
const Name = "speckit"

// Meta is this tool's declarative identity. It runs after uv via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Spec Kit",
	LaunchHelp:    "Launch Spec Kit",
	Group:         tools.GroupSystem,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{uv.Name},
}

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &speckitTool{
		Base:    tools.Base{Metadata: Meta},
		http:    params.HTTP.Unwrap(),
		logger:  params.Diagnostics.Logger("tools.speckit"),
		sandbox: params.Sandbox,
	}
	return result{Service: svc}
}

type speckitTool struct {
	tools.Base
	http    *http.Client
	logger  *diagnostic.Logger
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
		exists, err := helpers.CommandExists(
			ctx,
			t.sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"specify",
		)
		if err != nil || exists {
			return err
		}
	}

	tag, err := t.latestReleaseTag(ctx)
	if err != nil {
		return err
	}
	command := []string{"uv", "tool", "install", "specify-cli"}
	if force {
		command = append(command, "--force")
	}
	command = append(command, "--from", "git+"+repositoryURL+"@"+tag)
	_, err = t.sandbox.Exec(
		ctx,
		command,
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

func (t *speckitTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"specify"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}

func (t *speckitTool) latestReleaseTag(ctx context.Context) (string, error) {
	var data struct {
		TagName string `json:"tag_name"`
	}
	if err := kit.GetJSON(ctx, t.http, t.logger, latestReleaseURL, "application/vnd.github+json", &data); err != nil {
		return "", fmt.Errorf("failed to fetch latest Spec Kit release tag: %w", err)
	}
	if strings.TrimSpace(data.TagName) == "" {
		return "", fmt.Errorf("failed to resolve latest Spec Kit release tag: missing tag_name")
	}
	return strings.TrimSpace(data.TagName), nil
}
