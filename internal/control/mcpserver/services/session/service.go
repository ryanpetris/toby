// Package sessionservice contributes Toby's own MCP tools and introspection
// resources: the MCP sidecar lifecycle tools (mcp.start/stop/restart), the
// resources.read fallback, and the toby://session/* and toby://docs/* resources.
// Every resource is built from the session's non-secret state and routed through
// the runtime-info sanitizer, which strips URLs, headers, commands, argv, and env.
package sessionservice

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/control/mcpserver"
)

const mcpStartDescription = "Start a configured local Toby-managed MCP sidecar."

const mcpStopDescription = "Stop a configured local Toby-managed MCP sidecar."

const mcpRestartDescription = "Restart a configured local Toby-managed MCP sidecar."

const resourcesReadDescription = "Read one or more Toby resources by URI (for example toby://session/runtime or toby://docs/introspection). Pass uris to select specific resources; omit uris to read every available resource. Use this when the MCP client cannot read MCP resources directly."

// Service contributes Toby's introspection resources and lifecycle tools into the
// MCP server.
type Service struct{}

var _ mcpserver.Service = Service{}

func (Service) Tools() []mcpserver.Tool {
	return []mcpserver.Tool{
		{Name: "mcp.start", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "mcp.start", Description: mcpStartDescription}, handler{session}.mcpStart)
		}},
		{Name: "mcp.stop", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "mcp.stop", Description: mcpStopDescription}, handler{session}.mcpStop)
		}},
		{Name: "mcp.restart", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "mcp.restart", Description: mcpRestartDescription}, handler{session}.mcpRestart)
		}},
		{Name: "resources.read", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "resources.read", Description: resourcesReadDescription}, handler{session}.resourcesRead)
		}},
	}
}

func (Service) Resources() []mcpserver.Resource {
	return []mcpserver.Resource{
		{
			URI:         "toby://docs/mcps",
			Name:        "toby.docs.mcps",
			Title:       "Toby-Managed MCPs",
			Description: "Guidance for Toby-managed MCP proxying and lifecycle tools.",
			FS:          resourceDocs,
			FilePath:    "resources/mcps.md",
		},
		{
			URI:         "toby://docs/introspection",
			Name:        "toby.docs.introspection",
			Title:       "Toby Introspection",
			Description: "Guidance for Toby session introspection resources and redaction behavior.",
			FS:          resourceDocs,
			FilePath:    "resources/introspection.md",
		},
		{
			URI:         "toby://session/runtime",
			Name:        "toby.session.runtime",
			Title:       "Toby Session Runtime",
			Description: "Current Toby version, debug mode, sandbox runtime, and runtime paths.",
			Text: func(ctx context.Context, session *mcpserver.Session) (string, error) {
				return handler{session}.runtimeResource(ctx)
			},
		},
		{
			URI:         "toby://session/mcps",
			Name:        "toby.session.mcps",
			Title:       "Toby Session MCPs",
			Description: "Configured MCP status and redacted runtime details for this session.",
			Text: func(ctx context.Context, session *mcpserver.Session) (string, error) {
				return handler{session}.mcpsResource(ctx)
			},
		},
		{
			URI:         "toby://session/tools",
			Name:        "toby.session.tools",
			Title:       "Toby Session Tools",
			Description: "Active and available Toby tools plus provider summaries for this session.",
			Text: func(ctx context.Context, session *mcpserver.Session) (string, error) {
				return handler{session}.toolsResource(ctx)
			},
		},
		{
			URI:         "toby://session/projects",
			Name:        "toby.session.projects",
			Title:       "Toby Session Projects",
			Description: "Visible projects, additional binds, and managed mounts for this session.",
			Text: func(ctx context.Context, session *mcpserver.Session) (string, error) {
				return handler{session}.projectsResource(ctx)
			},
		},
	}
}
