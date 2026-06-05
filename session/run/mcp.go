package run

// MCP wiring for a launch: registerTobyMCPProxy publishes Toby's own MCP server
// behind the session HTTP proxy and returns its proxied URL; mcpDefaults computes
// the image/debug defaults applied to the MCP sidecar proxy.

import (
	"fmt"
	"strings"

	appconfig "petris.dev/toby/config/app"
	"petris.dev/toby/control"
	"petris.dev/toby/control/host"
	"petris.dev/toby/control/mcpproxy"
	"petris.dev/toby/control/mcpserver"
	gitservice "petris.dev/toby/control/mcpserver/services/git"
	"petris.dev/toby/tools"
)

func mcpDefaults(config *appconfig.Service) mcpproxy.Defaults {
	var defaults mcpproxy.Defaults
	if config != nil {
		defaults.Image = strings.TrimSpace(config.Image())
		defaults.Debug = config.DebugEnabled()
	}
	return defaults
}

func registerTobyMCPProxy(params Params, manager *host.Service, controlHost string, opts *tools.Options, activeTools []string, primary string) (string, error) {
	if params.MCPServer == nil {
		return "", fmt.Errorf("mcp server runner is not configured")
	}
	if manager == nil || manager.HTTPProxy == nil {
		return "", fmt.Errorf("http proxy service is not configured")
	}
	if strings.TrimSpace(controlHost) == "" {
		return "", fmt.Errorf("%s is required", control.EnvControlHost)
	}
	state := mcpserver.SessionState{Debug: params.TobyConfig.DebugEnabled(), Paths: params.Paths, Sandbox: params.SandboxService, MCPProxy: params.MCPProxy, Config: params.TobyConfig, Registry: params.Registry, ActiveTools: activeTools, PrimaryTool: primary}
	if opts != nil {
		state.Options = *opts
	}
	id, err := manager.HTTPProxy.RegisterHandler(params.MCPServer.Handler(gitservice.NewHostGitClient(manager), state))
	if err != nil {
		return "", err
	}
	return control.Endpoint{Host: controlHost}.ProxyBaseURL(id), nil
}
