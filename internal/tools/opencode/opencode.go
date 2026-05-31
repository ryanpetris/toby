package opencode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/config/toby"
	contextfiles "petris.dev/toby/internal/context/files"
	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/control/httpproxy"
	"petris.dev/toby/internal/diagnostic/warning"
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
	NPM          tool.Tool                `name:"npm"`
	Renderer     *opencodeconfig.Renderer `optional:"true"`
	Config       *tobyconfig.Service      `optional:"true"`
	Proxy        *httpproxy.Service       `optional:"true"`
	Sandbox      tool.SandboxService
	ContextFiles *contextfiles.Service
}

type Result struct {
	fx.Out

	Service  tool.Tool `name:"opencode"`
	Registry tool.Tool `group:"toby.tools"`
}

func Provide(params Params) Result {
	svc := &openCodeTool{
		Base:         toolutil.Base(tool.OpenCodeToolName, "Launch OpenCode", tool.GroupAI, tool.GroupSystem, tool.GroupVCS),
		paths:        params.Paths,
		npm:          params.NPM,
		renderer:     params.Renderer,
		config:       params.Config,
		proxy:        params.Proxy,
		sandbox:      params.Sandbox,
		contextFiles: params.ContextFiles,
	}
	return Result{Service: svc, Registry: svc}
}

type openCodeTool struct {
	tool.Base
	paths        config.Paths
	npm          tool.Tool
	renderer     *opencodeconfig.Renderer
	config       *tobyconfig.Service
	proxy        *httpproxy.Service
	sandbox      tool.SandboxService
	contextFiles *contextfiles.Service
}

func (t *openCodeTool) deps() []tool.Tool { return []tool.Tool{t.npm} }

func (t *openCodeTool) PathEntries() []tool.PathTarget {
	return toolutil.PathEntries(t.deps(), nil)
}

func (t *openCodeTool) HostInit(ctx context.Context, opts *tool.CommandOptions) error {
	if err := toolutil.HostInitDependencies(ctx, opts, t.npm); err != nil {
		return err
	}
	if opts.ToolStateFor(t.Name()) != tool.ToolStateHost {
		return nil
	}
	return tool.HostInitOnce(opts, t.Name(), func() error {
		for _, dir := range t.stateDirs(opts.ToolStateRootFor(t.Name())) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		return nil
	})
}

func (t *openCodeTool) Binds() []tool.Bind {
	own := []tool.Bind{
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".config", "opencode"), Target: tool.HomeTarget(".config", "opencode"), Type: tool.BindRegular, State: true, StatePath: filepath.ToSlash(filepath.Join(".config", "opencode"))},
		{HostPath: filepath.Join(t.paths.SandboxRoot, ".local", "share", "opencode"), Target: tool.HomeTarget(".local", "share", "opencode"), Type: tool.BindRegular, State: true, StatePath: filepath.ToSlash(filepath.Join(".local", "share", "opencode"))},
	}
	return toolutil.Binds(t.deps(), own)
}

func (t *openCodeTool) stateDirs(root string) []string {
	return []string{
		filepath.Join(root, ".config", "opencode"),
		filepath.Join(root, ".local", "share", "opencode"),
	}
}

func (t *openCodeTool) SandboxContextSetup(ctx context.Context) error {
	if err := toolutil.SandboxContextSetupDependencies(ctx, t.npm); err != nil {
		return err
	}
	return tool.SandboxContextSetupOnce(ctx, t.Name(), func() error {
		return t.sandbox.SetEnvironment(ctx, "OPENCODE_CONFIG_DIR", filepath.Join(t.sandbox.Paths().Context, "opencode"))
	})
}

func (t *openCodeTool) SandboxInit(ctx context.Context) error {
	return toolutil.SandboxInitDependencies(ctx, t.npm)
}

func (t *openCodeTool) RegisterContextFiles(ctx context.Context, opts tool.ContextOptions) error {
	return tool.RegisterContextFilesOnce(ctx, t.Name(), func() error {
		if t.renderer == nil {
			return fmt.Errorf("opencode renderer is not configured")
		}
		if registrar, ok := t.npm.(tool.ContextFileTool); ok {
			if err := registrar.RegisterContextFiles(ctx, opts); err != nil {
				return err
			}
		}
		controlHost, _ := t.sandbox.GetEnvironment(control.EnvControlHost)
		warnings, err := t.renderer.RegisterContextFiles(ctx, t.contextFiles.Registrar(ctx), t.sandbox.Paths().Workspace, controlHost, t.sandbox.TobyMCPURL(), t.contextFiles.InstructionPaths(), t.config, t.proxy)
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
	if err := toolutil.InstallDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.install(ctx, false)
}

func (t *openCodeTool) Upgrade(ctx context.Context) error {
	if err := toolutil.UpgradeDependencies(ctx, t.npm); err != nil {
		return err
	}
	return t.install(ctx, true)
}

func (t *openCodeTool) install(ctx context.Context, force bool) error {
	once := tool.InstallOnce
	if force {
		once = tool.UpgradeOnce
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
