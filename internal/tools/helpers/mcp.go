package helpers

// Shared stdio/HTTP MCP configuration rendering for tools that use the
// mcpServers JSON object shape.

import (
	"fmt"

	sessionconfig "petris.dev/toby/internal/config/session"
)

// StdioHTTPMCPServers renders stdio and HTTP MCP servers as an mcpServers
// object. The tool name is used only in error messages.
func StdioHTTPMCPServers(
	servers []sessionconfig.MCPServer,
	tool string,
) (map[string]any, error) {
	out := map[string]any{}
	for _, server := range servers {
		if err := server.Validate(); err != nil {
			return nil, fmt.Errorf(
				"render %s MCP server %q: %w",
				tool,
				server.Name,
				err,
			)
		}

		switch server.Transport {
		case sessionconfig.MCPTransportStdio:
			out[server.Name] = map[string]any{
				"type":    "stdio",
				"command": server.Command,
				"args":    append([]string(nil), server.Args...),
			}
		case sessionconfig.MCPTransportHTTP:
			out[server.Name] = map[string]any{
				"type": "http",
				"url":  server.URL,
			}
		default:
			return nil, fmt.Errorf(
				"render %s MCP server %q: unsupported transport %q",
				tool,
				server.Name,
				server.Transport,
			)
		}
	}
	return map[string]any{"mcpServers": out}, nil
}
