package proxyconfig

import (
	"fmt"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/httpproxy"
	"petris.dev/toby/internal/tobyconfig"
)

func MCPURL(controlHost string, proxy *httpproxy.Service, server tobyconfig.MCPServer) (string, error) {
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
	id, err := proxy.Register(httpproxy.Target{BaseURL: server.URL(), Headers: headers})
	if err != nil {
		return "", err
	}
	return control.Endpoint{Host: controlHost}.ProxyBaseURL(id), nil
}
