// Package opencode provides the OpenCode agent tool: it installs opencode-ai via
// npm and launches it with Toby's generated opencode.json (MCP servers,
// providers, instructions, and permission paths).
package opencode

import (
	"context"
	"path/filepath"

	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	"petris.dev/toby/internal/tools/builtin/npm"
	opencodeconfig "petris.dev/toby/internal/tools/builtin/opencode/config"
	"petris.dev/toby/internal/tools/helpers"
)

// Name is this tool's canonical identifier.
const Name = "opencode"

// Meta is this tool's declarative identity. It runs after npm via its dependency.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "OpenCode",
	LaunchHelp:    "Launch OpenCode",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
	Dependencies:  []string{npm.Name},
}

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &openCodeTool{
		Base:          tools.Base{Metadata: Meta},
		sessionConfig: params.SessionConfig,
		sandbox:       params.Sandbox,
	}
	return result{Service: svc}
}

type openCodeTool struct {
	tools.Base
	sessionConfig *sessionconfig.Holder
	sandbox       sandbox.Service
}

var _ tools.Tool = (*openCodeTool)(nil)
var _ toolfiles.Contributor = (*openCodeTool)(nil)

func (t *openCodeTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	for _, req := range t.mounts() {
		if err := t.sandbox.AddMount(req); err != nil {
			return err
		}
	}
	return nil
}

func (t *openCodeTool) mounts() []mount.Request {
	return []mount.Request{
		{Key: mount.Key{Type: mount.TypeTool, Name: t.Name(), Purpose: "config"}, Target: "~/.config/opencode", Access: mount.AccessRegular},
		{Key: mount.Key{Type: mount.TypeTool, Name: t.Name(), Purpose: "data"}, Target: "~/.local/share/opencode", Access: mount.AccessRegular},
	}
}

func (t *openCodeTool) ConfigureSandbox(ctx context.Context) error {
	return t.sandbox.SetEnvironment(
		ctx,
		"OPENCODE_CONFIG_DIR",
		filepath.Dir(opencodeconfig.NativePriorityConfigPath),
	)
}

func (t *openCodeTool) InitSandbox(ctx context.Context) error {
	return nil
}

func (t *openCodeTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return opencodeconfig.NativeFiles(Name, ownership, cfg)
}

func (t *openCodeTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			"opencode",
		)
		if err != nil || exists {
			return err
		}
	}
	_, err := t.sandbox.Exec(
		ctx,
		[]string{"npm", "install", "-g", "opencode-ai"},
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

func (t *openCodeTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"opencode"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
