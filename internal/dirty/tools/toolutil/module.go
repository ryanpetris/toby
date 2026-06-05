package toolutil

import (
	"net/http"
	"time"

	"petris.dev/toby/config"
	"petris.dev/toby/sandbox"
	"petris.dev/toby/tools"

	"go.uber.org/fx"
)

var Module = fx.Provide(NewHTTPClient)

func NewHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// Base builds a tool identity. group is the tool's primary category (used for
// listing and group expansion) and is also its first context group; contextGroups
// are the additional groups whose context the tool receives.
func Base(name, help, group string, contextGroups ...string) tools.Base {
	return tools.Base{Metadata: tools.Metadata{Name: name, LaunchHelp: help, Group: group, ContextGroups: append([]string{group}, contextGroups...)}}
}

func DependentBase(name, help string, priority int, dependencies []string, group string, contextGroups ...string) tools.Base {
	return tools.Base{Metadata: tools.Metadata{Name: name, LaunchHelp: help, Group: group, ContextGroups: append([]string{group}, contextGroups...), Dependencies: append([]string(nil), dependencies...), Priority: priority}}
}

func NewSimple(paths config.Paths, sbx sandbox.Service, base tools.Base, hostSubpath, sandboxSubpath []string, install []string, sandboxEnv map[string]string) *Simple {
	return &Simple{
		Base:           base,
		Sandbox:        sbx,
		RootDir:        paths.SandboxRoot,
		HostSubpath:    hostSubpath,
		SandboxSubpath: sandboxSubpath,
		InstallCommand: install,
		SandboxEnv:     sandboxEnv,
	}
}
