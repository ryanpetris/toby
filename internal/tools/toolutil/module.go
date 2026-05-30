package toolutil

import (
	"context"
	"net/http"
	"time"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/tool"

	"go.uber.org/fx"
)

var Module = fx.Provide(NewHTTPClient)

func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func Base(name, help string, groups ...string) tool.Base {
	return tool.Base{Metadata: tool.Metadata{Name: name, LaunchHelp: help, ContextGroups: groups}}
}

func Simple(paths config.Paths, base tool.Base, hostSubpath, sandboxSubpath []string, install []string, sandboxEnv map[string]string) *tool.Simple {
	return &tool.Simple{
		Base:           base,
		RootDir:        paths.SandboxRoot,
		HostSubpath:    hostSubpath,
		SandboxSubpath: sandboxSubpath,
		InstallCommand: install,
		SandboxEnv:     sandboxEnv,
	}
}

func Binds(deps []tool.Tool, own []tool.Bind) []tool.Bind {
	var binds []tool.Bind
	seen := map[tool.Bind]bool{}
	for _, dep := range deps {
		for _, bind := range dep.Binds() {
			if seen[bind] {
				continue
			}
			seen[bind] = true
			binds = append(binds, bind)
		}
	}
	for _, bind := range own {
		if seen[bind] {
			continue
		}
		seen[bind] = true
		binds = append(binds, bind)
	}
	return binds
}

func PathEntries(deps []tool.Tool, own []tool.PathTarget) []tool.PathTarget {
	var entries []tool.PathTarget
	seen := map[tool.PathTarget]bool{}
	for _, dep := range deps {
		for _, entry := range dep.PathEntries() {
			if seen[entry] {
				continue
			}
			seen[entry] = true
			entries = append(entries, entry)
		}
	}
	for _, entry := range own {
		if seen[entry] {
			continue
		}
		seen[entry] = true
		entries = append(entries, entry)
	}
	return entries
}

func HostInitDependencies(ctx context.Context, opts *tool.CommandOptions, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.HostInit(ctx, opts); err != nil {
			return err
		}
	}
	return nil
}

func SandboxContextSetupDependencies(ctx *tool.RunContext, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.SandboxContextSetup(ctx); err != nil {
			return err
		}
	}
	return nil
}

func SandboxInitDependencies(ctx context.Context, run *tool.RunContext, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.SandboxInit(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func InstallDependencies(ctx context.Context, run *tool.RunContext, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.Install(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

func UpgradeDependencies(ctx context.Context, run *tool.RunContext, deps ...tool.Tool) error {
	for _, dep := range deps {
		if err := dep.Upgrade(ctx, run); err != nil {
			return err
		}
	}
	return nil
}
