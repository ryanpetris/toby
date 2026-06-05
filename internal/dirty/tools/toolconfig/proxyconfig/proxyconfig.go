package proxyconfig

import (
	"fmt"

	"petris.dev/toby/config/toby"
	"petris.dev/toby/control"
	"petris.dev/toby/control/httpproxy"
	"petris.dev/toby/internal/dirty/control/mcpproxy"
)

func MCPURL(controlHost string, proxy *httpproxy.Service, manager *mcpproxy.Service, name string, server tobyconfig.MCPServer) (string, error) {
	if manager != nil {
		if url, ok := manager.URL(name); ok {
			return url, nil
		}
	}
	if server.Local() {
		return "", fmt.Errorf("local MCP requires mcp proxy service")
	}
	if controlHost == "" {
		return "", fmt.Errorf("remote MCP requires %s", control.EnvControlHost)
	}
	if proxy == nil {
		return "", fmt.Errorf("remote MCP requires http proxy service")
	}
	headers, err := server.Headers()
	if err != nil {
		return "", err
	}
	id, err := proxy.RegisterUpstream(server.URL(), headers)
	if err != nil {
		return "", err
	}
	return control.Endpoint{Host: controlHost}.ProxyBaseURL(id), nil
}
