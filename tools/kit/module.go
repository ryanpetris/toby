// Package kit provides shared building blocks for the concrete tool
// implementations: the Simple tool template, Base metadata constructors, a
// shared HTTP client for downloads, and GitHub release/asset-architecture
// helpers.
package kit

import (
	"net/http"
	"time"

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

func NewSimple(sbx sandbox.Service, base tools.Base, sandboxSubpath []string, install []string, sandboxEnv map[string]string) *Simple {
	return &Simple{
		Base:           base,
		Sandbox:        sbx,
		SandboxSubpath: sandboxSubpath,
		InstallCommand: install,
		SandboxEnv:     sandboxEnv,
	}
}
