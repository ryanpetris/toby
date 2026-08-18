// Package cursor provides the Cursor CLI agent tool: it installs cursor-agent
// into the sandbox and launches it with Toby's generated MCP config and
// instructions.
package cursor

import (
	"context"
	"fmt"
	pathpkg "path"
	"path/filepath"
	"runtime"

	appconfig "petris.dev/toby/internal/config/app"
	sessionconfig "petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/runtimeassets"
	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandbox/mount"
	"petris.dev/toby/internal/toolfiles"
	"petris.dev/toby/internal/tools"
	cursorconfig "petris.dev/toby/internal/tools/builtin/cursor/config"
	"petris.dev/toby/internal/tools/helpers"
	"petris.dev/toby/internal/tools/kit"
	"petris.dev/toby/internal/tools/runtimepath"
)

// Name is this tool's canonical identifier.
const Name = "cursor"

// Meta is this tool's declarative identity.
var Meta = tools.Metadata{
	Name:          Name,
	DisplayName:   "Cursor",
	LaunchHelp:    "Launch Cursor",
	Group:         tools.GroupAI,
	ContextGroups: []string{tools.GroupAI, tools.GroupSystem, tools.GroupVCS},
}

const (
	cursorInstallPath = "cursor/install.sh"
	cursorCommand     = "cursor-agent"
)

// provide constructs the tool implementation.
func provide(params params) result {
	svc := &cursorTool{
		Simple: kit.NewSimple(
			params.Sandbox,
			tools.Base{Metadata: Meta},
			[]string{".cursor"},
			nil,
			map[string]string{
				"CURSOR_CONFIG_DIR":          filepath.Join(layout.Home, ".cursor"),
				"AGENT_CLI_CREDENTIAL_STORE": "file",
			},
		),
		sessionConfig: params.SessionConfig,
		config:        params.Config,
	}
	return result{Service: svc}
}

type cursorTool struct {
	*kit.Simple
	sessionConfig *sessionconfig.Holder
	config        *appconfig.LaunchHolder
	yolo          bool
}

var _ tools.Tool = (*cursorTool)(nil)
var _ runtimeassets.Contributor = (*cursorTool)(nil)
var _ toolfiles.Contributor = (*cursorTool)(nil)

func (t *cursorTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	current := t.config.Current()
	t.yolo = current != nil && current.Settings().YoloEnabled()

	if err := t.Simple.PrepareHost(ctx, opts); err != nil {
		return err
	}
	for _, req := range extraMounts() {
		if err := t.Sandbox.AddMount(req); err != nil {
			return err
		}
	}
	return nil
}

func extraMounts() []mount.Request {
	return []mount.Request{
		{
			Key:    mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "config"},
			Target: "~/.config/cursor",
			Access: mount.AccessRegular,
		},
		{
			Key:    mount.Key{Type: mount.TypeTool, Name: Name, Purpose: "data"},
			Target: "~/.local/share/cursor-agent",
			Access: mount.AccessRegular,
		},
	}
}

func (t *cursorTool) ToolFiles(ownership toolfiles.Ownership) ([]toolfiles.File, error) {
	cfg, err := t.sessionConfig.Config()
	if err != nil {
		return nil, err
	}

	return cursorconfig.NativeFiles(Name, ownership, cfg)
}

func (t *cursorTool) RuntimeAssets() ([]runtimeassets.Asset, error) {
	data, err := cursorFiles.ReadFile("resources/install.sh")
	if err != nil {
		return nil, err
	}

	return []runtimeassets.Asset{{
		Target: pathpkg.Join(layout.Runtime, cursorInstallPath),
		Data:   data,
		Mode:   0o755,
	}}, nil
}

func (t *cursorTool) ConfigureSandbox(ctx context.Context) error {
	if err := t.Simple.ConfigureSandbox(ctx); err != nil {
		return err
	}

	return t.Sandbox.AppendEnvironment(ctx, "PATH", filepath.Join(layout.Home, ".local", "bin"), ":")
}

func (t *cursorTool) Install(ctx context.Context, force bool) error {
	if !force {
		exists, err := helpers.CommandExists(
			ctx,
			t.Sandbox.Exec,
			sandbox.ExecOptions{
				HideOutput: true,
				Status:     "Checking installation",
			},
			cursorCommand,
		)
		if err != nil || exists {
			return err
		}
	}

	arch, err := assetArch()
	if err != nil {
		return err
	}
	installer, err := runtimepath.Resolve(cursorInstallPath)
	if err != nil {
		return err
	}
	_, err = t.Sandbox.Exec(
		ctx,
		[]string{installer, arch},
		sandbox.ExecOptions{Status: "Installing"},
	)
	return err
}

func (t *cursorTool) Launch(ctx context.Context, extra []string) error {
	// cursor-agent is the official binary. The agent alias would collide with
	// Grok's agent symlink when both tools share a sandbox.
	argv := []string{cursorCommand, "--approve-mcps", "--sandbox", "disabled"}
	if t.yolo {
		argv = append(argv, "--force")
	}
	argv = append(argv, extra...)
	_, err := t.Sandbox.Exec(ctx, argv, sandbox.ExecOptions{Foreground: true})
	return err
}

func assetArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported platform for cursor: %s", runtime.GOARCH)
	}
}
