package toolutil

import (
	"context"
	"net/http"
	"time"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tools/tool"

	"go.uber.org/fx"
)

var Module = fx.Provide(NewHTTPClient)

func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func Base(name, help string, groups ...string) tool.Base {
	return tool.Base{Metadata: tool.Metadata{Name: name, LaunchHelp: help, ContextGroups: groups}}
}

func Simple(paths config.Paths, sandbox tool.SandboxService, base tool.Base, hostSubpath, sandboxSubpath []string, install []string, sandboxEnv map[string]string) *tool.Simple {
	return &tool.Simple{
		Base:           base,
		Sandbox:        sandbox,
		RootDir:        paths.SandboxRoot,
		HostSubpath:    hostSubpath,
		SandboxSubpath: sandboxSubpath,
		InstallCommand: install,
		SandboxEnv:     sandboxEnv,
	}
}

func HostInitDependencies(ctx context.Context, opts *tool.CommandOptions, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.HostInit(ctx, opts); err != nil {
			return err
		}
	}
	return nil
}

func SandboxContextSetupDependencies(ctx context.Context, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.SandboxContextSetup(ctx); err != nil {
			return err
		}
	}
	return nil
}

func SandboxInitDependencies(ctx context.Context, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.SandboxInit(ctx); err != nil {
			return err
		}
	}
	return nil
}

func InstallDependencies(ctx context.Context, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.Install(ctx); err != nil {
			return err
		}
	}
	return nil
}

func UpgradeDependencies(ctx context.Context, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.Upgrade(ctx); err != nil {
			return err
		}
	}
	return nil
}
