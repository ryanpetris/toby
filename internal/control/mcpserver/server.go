// Package mcpserver is the host-side MCP server Toby exposes to the tools running
// inside a sandbox. It composes per-session "services" (the built-in git and Toby
// services) into a single streamable-HTTP MCP server: host Git tools, MCP sidecar
// lifecycle tools, a resources.read fallback, and the toby:// session and docs
// introspection resources (which redact URLs, headers, commands, argv, and env).
package mcpserver

import (
	"fmt"
	"net/http"

	"petris.dev/toby/internal/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
)

type RunnerParams struct {
	fx.In

	Services []Service `group:"mcp.services"`
}

type Runner struct {
	tools     []Tool
	resources []Resource
}

func NewRunner(params RunnerParams) (*Runner, error) {
	seenTools := map[string]bool{}
	seenResources := map[string]bool{}
	var tools []Tool
	var resources []Resource
	for _, service := range params.Services {
		if service == nil {
			continue
		}
		for _, tool := range service.Tools() {
			if tool.Name == "" {
				return nil, fmt.Errorf("mcp tool must define a name")
			}
			if tool.Register == nil {
				return nil, fmt.Errorf("mcp tool %s must define a register function", tool.Name)
			}
			if seenTools[tool.Name] {
				return nil, fmt.Errorf("duplicate mcp tool: %s", tool.Name)
			}
			seenTools[tool.Name] = true
			tools = append(tools, tool)
		}
		for _, resource := range service.Resources() {
			if err := validateResource(resource); err != nil {
				return nil, err
			}
			if seenResources[resource.URI] {
				return nil, fmt.Errorf("duplicate mcp resource: %s", resource.URI)
			}
			seenResources[resource.URI] = true
			resources = append(resources, resource)
		}
	}
	return &Runner{tools: tools, resources: resources}, nil
}

const serverInstructions = `Toby runs development tools inside private-home sandboxes and exposes this MCP server for host-side operations and Toby session context.

Read Toby MCP resources when you need guidance or current session details:
- toby://docs/git explains host Git tools and default Git workflow expectations.
- toby://docs/mcps explains Toby-managed MCP sidecars and lifecycle tools.
- toby://docs/introspection explains the session resources and redaction behavior.
- toby://session/runtime returns the current Toby version, debug mode, sandbox runtime, and runtime paths.
- toby://session/mcps returns configured MCP status and redacted runtime details.
- toby://session/tools returns active and available Toby tools plus provider summaries.
- toby://session/projects returns visible projects, binds, and managed mounts.

If your client cannot read MCP resources directly, call the resources.read tool with the resource URIs (or no arguments to read them all) to get the same content.

Use Git tools for repositories visible in the sandbox when host Git config, SSH agents, GPG signing, or credential helpers are needed. Use MCP lifecycle tools only for Toby-managed local MCP sidecars. Toby introspection never exposes provider or MCP URLs, headers, commands, argv, or environment values.`

func (r *Runner) Handler(client GitClient, state SessionState) http.Handler {
	session := &Session{Git: client, State: state.Clone(), Resources: r.resources}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return r.server(session)
	}, nil)
}

func (r *Runner) server(session *Session) *mcp.Server {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "toby", Version: version.String()}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})
	for _, tool := range r.tools {
		tool.Register(mcpServer, session)
	}
	for _, resource := range r.resources {
		resource.Register(mcpServer, session)
	}
	return mcpServer
}
