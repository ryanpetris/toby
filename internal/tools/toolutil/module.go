package toolutil

import (
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

func DependentBase(name, help string, priority int, dependencies []string, groups ...string) tool.Base {
	return tool.Base{Metadata: tool.Metadata{Name: name, LaunchHelp: help, ContextGroups: groups, Dependencies: append([]string(nil), dependencies...), Priority: priority}}
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
