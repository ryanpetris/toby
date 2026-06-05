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

func NewSimple(sbx sandbox.Service, base tools.Base, sandboxSubpath []string, install []string, sandboxEnv map[string]string) *Simple {
	return &Simple{
		Base:           base,
		Sandbox:        sbx,
		SandboxSubpath: sandboxSubpath,
		InstallCommand: install,
		SandboxEnv:     sandboxEnv,
	}
}
