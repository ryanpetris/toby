package sessionconfig

// Defines the sandbox-safe MCP transport descriptors rendered into native tool
// configuration.

import (
	"fmt"
	"strings"
)

// MCPTransport is the application-facing transport used to reach one resolved
// MCP server.
type MCPTransport string

const (
	// MCPTransportStdio selects an MCP stdio transport.
	MCPTransportStdio MCPTransport = "stdio"
	// MCPTransportHTTP selects an MCP streamable HTTP transport.
	MCPTransportHTTP MCPTransport = "http"
)

// MCPServer describes one MCP server without exposing its real upstream
// endpoint or credentials. Stdio entries normally invoke
// "/toby/bin/tobys resource connect -- <client-resource-id>"; HTTP entries
// contain
// only a protected capability URL.
type MCPServer struct {
	Name      string
	Transport MCPTransport
	Command   string
	Args      []string
	URL       string
}

// Validate rejects incomplete and ambiguous descriptors.
func (s MCPServer) Validate() error {
	if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Name) != s.Name {
		return fmt.Errorf("MCP server name must be non-empty and have no surrounding whitespace")
	}
	if strings.ContainsRune(s.Name, '\x00') {
		return fmt.Errorf("MCP server name contains NUL")
	}

	switch s.Transport {
	case MCPTransportStdio:
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("stdio MCP server %q requires a command", s.Name)
		}
		if s.URL != "" {
			return fmt.Errorf("stdio MCP server %q must not have a URL", s.Name)
		}
		for i, arg := range s.Args {
			if strings.ContainsRune(arg, '\x00') {
				return fmt.Errorf("stdio MCP server %q argument %d contains NUL", s.Name, i)
			}
		}
	case MCPTransportHTTP:
		if strings.TrimSpace(s.URL) == "" {
			return fmt.Errorf("HTTP MCP server %q requires a URL", s.Name)
		}
		if s.Command != "" || len(s.Args) != 0 {
			return fmt.Errorf("HTTP MCP server %q must not have a command", s.Name)
		}
	default:
		return fmt.Errorf("MCP server %q has unsupported transport %q", s.Name, s.Transport)
	}

	return nil
}

func (s MCPServer) clone() MCPServer {
	s.Args = append([]string(nil), s.Args...)
	return s
}
