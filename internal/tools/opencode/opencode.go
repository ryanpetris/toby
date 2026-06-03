package opencode

import (
	"context"
	"fmt"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/control/mcpproxy"
	"petris.dev/toby/internal/diagnostic/warning"
	sandboxmount "petris.dev/toby/internal/sandbox/mount"
	sandboxpath "petris.dev/toby/internal/sandbox/path"
	"petris.dev/toby/internal/tools/helpers"
	opencodeconfig "petris.dev/toby/internal/tools/opencode/config"
	"petris.dev/toby/internal/tools/tool"
	"petris.dev/toby/internal/tools/toolutil"

	"go.uber.org/fx"
)

var Module = fx.Module("tools.opencode", fx.Provide(opencodeconfig.NewRenderer, Provide))

type Params struct {
	fx.In

	Paths        config.Paths
	Renderer     *opencodeconfig.Renderer `optional:"true"`
	Config       *tobyconfig.Service      `optional:"true"`
	Proxy        *httpproxy.Service       `optional:"true"`
	MCPProxy     *mcpproxy.Service        `optional:"true"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &openCodeTool{
		Base:         toolutil.DependentBase(tool.OpenCodeToolName, "Launch OpenCode", 100, []string{tool.NpmToolName}, tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
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
	tool.Base
	renderer     *opencodeconfig.Renderer
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	mcpProxy     *mcpproxy.Service
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *openCodeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	return helpers.HostInitOnce(opts, t.Name(), func() error {
		for _, req := range t.mounts() {
			if _, err := t.sandbox.AddMount(req); err != nil {
				return err
			}
		}
		return nil
	})
}
func (t *openCodeTool) mounts() []sandboxmount.Request {
	return []sandboxmount.Request{
		{Key: sandboxmount.Key{Type: sandboxmount.TypeTool, Name: t.Name(), Purpose: "config"}, Target: sandboxpath.HomePath(".config", "opencode")},
		{Key: sandboxmount.Key{Type: sandboxmount.TypeTool, Name: t.Name(), Purpose: "data"}, Target: sandboxpath.HomePath(".local", "share", "opencode")},
	}
}

func (t *openCodeTool) SandboxContextSetup(ctx context.Context) error {
	return helpers.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		return t.sandbox.SetEnvironment(ctx, "OPENCODE_CONFIG_DIR", filepath.Join(t.sandbox.Paths().Context, "opencode"))
	})
}

func (t *openCodeTool) SandboxInit(ctx context.Context) error {
	return nil
}

func (t *openCodeTool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return helpers.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		if t.renderer == nil {
			return fmt.Errorf("opencode renderer is not configured")
		}
		controlHost, _ := t.sandbox.GetEnvironment(control.EnvControlHost)
		warnings, err := t.renderer.RegisterContextFiles(ctx, t.contextFiles.Registrar(ctx), t.sandbox.Paths(), controlHost, t.sandbox.TobyMCPURL(), t.contextFiles.InstructionPaths(), t.config, t.proxy, t.mcpProxy)
		if err != nil {
			return err
		}
		for _, item := range warnings {
			warning.Fprintf(opts.Stderr, opts.SuppressWarnings, warning.OpenCodeModelDiscovery, "failed to fetch OpenCode models: %v", item)
		}
		return nil
	})
}

func (t *openCodeTool) Install(ctx context.Context) error {
	return t.install(ctx, false)
}

func (t *openCodeTool) Upgrade(ctx context.Context) error {
	return t.install(ctx, true)
}

func (t *openCodeTool) install(ctx context.Context, force bool) error {
	once := helpers.InstallOnce
	if force {
		once = helpers.UpgradeOnce
	}
	return once(ctx, t.Name(), func() error {
		if !force {
			exists, err := helpers.CommandExists(ctx, t.sandbox.Exec, tool.ExecOptions{HideOutput: true}, "opencode")
			if err != nil || exists {
				return err
			}
		}
		_, err := t.sandbox.Exec(ctx, []string{"npm", "install", "-g", "opencode-ai"}, tool.ExecOptions{})
		return err
	})
}

func (t *openCodeTool) Launch(ctx context.Context, extra []string) error {
	_, err := t.sandbox.Exec(ctx, append([]string{"opencode"}, extra...), tool.ExecOptions{Foreground: true})
	return err
}
