package opencode

import (
	"context"
	"fmt"
	"path/filepath"

	"petris.dev/toby/config"
	"petris.dev/toby/config/toby"
	"petris.dev/toby/container/layout"
	"petris.dev/toby/container/mount"
	contextfiles "petris.dev/toby/context/files"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/diagnostic/warning"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
	"petris.dev/toby/internal/dirty/tools/helpers"
	opencodeconfig "petris.dev/toby/internal/dirty/tools/opencode/config"
	"petris.dev/toby/internal/dirty/tools/toolutil"
	"petris.dev/toby/providers"
	"petris.dev/toby/providers/anthropic"
	"petris.dev/toby/providers/openai"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.opencode",
	providers.Module(),
	openai.Module(),
	anthropic.Module(),
	fx.Provide(opencodeconfig.NewRenderer, Provide),
)

type Params struct {
	fx.In

	Paths        config.Paths
	Renderer     *opencodeconfig.Renderer `optional:"true"`
	Config       *tobyconfig.Service      `optional:"true"`
	Proxy        *httpproxy.Service       `optional:"true"`
	MCPProxy     *mcpproxy.Service        `optional:"true"`
	Sandbox      sandbox.Service
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tools.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &openCodeTool{
		Base:         toolutil.DependentBase(tools.OpenCodeToolName, "Launch OpenCode", 100, []string{tools.NpmToolName}, tools.GroupAI, tools.GroupSystem, tools.GroupVCS),
		renderer:     params.Renderer,
		config:       params.Config,
		proxy:        params.Proxy,
		mcpProxy:     params.MCPProxy,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc}
}

type openCodeTool struct {
	tools.Base
	renderer     *opencodeconfig.Renderer
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	sandbox      sandbox.Service
	contextFiles *contextfiles.Service
}

func (t *openCodeTool) PrepareHost(ctx context.Context, opts *tools.Options) error {
	return helpers.HostInitOnce(opts, t.Name(), func() error {
		for _, req := range t.mounts() {
			if _, err := t.sandbox.AddMount(req); err != nil {
				return err
			}
		}
		return nil
	})
}
func (t *openCodeTool) mounts() []mount.Request {
	return []mount.Request{
		{Key: mount.Key{Type: mount.TypeTool, Name: t.Name(), Purpose: "config"}, Target: "~/.config/opencode"},
		{Key: mount.Key{Type: mount.TypeTool, Name: t.Name(), Purpose: "data"}, Target: "~/.local/share/opencode"},
	}
}

func (t *openCodeTool) ConfigureSandbox(ctx context.Context) error {
	return helpers.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		return t.sandbox.SetEnvironment(ctx, "OPENCODE_CONFIG_DIR", filepath.Join(layout.Context, "opencode"))
	})
}

func (t *openCodeTool) InitSandbox(ctx context.Context) error {
	return nil
}

func (t *openCodeTool) RegisterContextFiles(ctx context.Context, opts tools.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		if t.renderer == nil {
			return fmt.Errorf("opencode renderer is not configured")
		}
		controlHost, _ := t.sandbox.GetEnvironment(control.EnvControlHost)
		warnings, err := t.renderer.RegisterContextFiles(ctx, t.contextFiles.Registrar(ctx), controlHost, t.sandbox.TobyMCPURL(), t.contextFiles.InstructionPaths(), t.config, t.proxy, t.mcpProxy)
		if err != nil {
			return err
		}
		for _, item := range warnings {
			warning.Fprintf(opts.Stderr, opts.SuppressWarnings, warning.OpenCodeModelDiscovery, "failed to fetch OpenCode models: %v", item)
		}
		return nil
	})
}

func (t *openCodeTool) Install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, sandbox.ExecOptions{HideOutput: true}, "opencode")
			if err != nil || exists {
				return err
			}
		}
		_, err := t.sandbox.Exec(ctx, []string{"npm", "install", "-g", "opencode-ai"}, sandbox.ExecOptions{})
		return err
	})
}

func (t *openCodeTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"opencode"}, extra...), sandbox.ExecOptions{Foreground: true})
	return err
}
