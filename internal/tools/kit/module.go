// Package kit provides shared building blocks for the concrete tool
// implementations: the Simple tool template, Base metadata constructors, a
// shared HTTP client for downloads, and GitHub release/asset-architecture
// helpers.
package kit

import (
	"net/http"
	"time"

	"petris.dev/toby/internal/sandbox"
	"petris.dev/toby/internal/tools"

	"go.uber.org/fx"
)

// Module provides the package's components to Fx.
var Module = fx.Provide(NewHTTPClient)

// HTTPClient distinguishes tool download traffic from other process HTTP
// clients in the static Fx graph.
type HTTPClient struct {
	*http.Client
}

// NewHTTPClient constructs the HTTP client used for tool downloads.
func NewHTTPClient() *HTTPClient {
	return &HTTPClient{Client: &http.Client{Timeout: 30 * time.Second}}
}

// Unwrap returns the standard-library client, or nil for an absent wrapper.
func (c *HTTPClient) Unwrap() *http.Client {
	if c == nil {
		return nil
	}
	return c.Client
}

// NewSimple constructs a tool with declarative installation and launch behavior.
func NewSimple(sbx sandbox.Service, base tools.Base, sandboxSubpath []string, install []string, sandboxEnv map[string]string) *Simple {
	return &Simple{
		Base:           base,
		Sandbox:        sbx,
		SandboxSubpath: sandboxSubpath,
		InstallCommand: install,
		SandboxEnv:     sandboxEnv,
	}
}
