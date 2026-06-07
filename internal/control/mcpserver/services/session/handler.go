package sessionservice

// handler binds the per-session context for one tool or resource invocation: it
// serves the resources.read fallback, renders the toby://session/* introspection
// resources, and runs the mcp.start/stop/restart sidecar lifecycle tools.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/approval"
	"petris.dev/toby/internal/control/mcpserver"
	"petris.dev/toby/internal/permission"
	"petris.dev/toby/internal/version"
)

// handler binds the per-session context for one tool or resource invocation.
type handler struct {
	session *mcpserver.Session
}

// approve consults the approval service for an action, supplying its default rule; it
// returns an error (denying the tool call) when refused, or nil when allowed or when no
// approval service is wired.
func (h handler) approve(ctx context.Context, action, name, message string, def permission.Rule) error {
	svc := h.session.State.Approval
	if svc == nil {
		return nil
	}
	decision, err := svc.Request(ctx, approval.Request{Action: action, Name: name, Message: message, Default: def})
	if err != nil {
		return err
	}
	if decision != permission.Allow {
		return fmt.Errorf("permission denied")
	}
	return nil
}

// resourcesRead returns the contents of the named toby:// resources, mirroring
// the MCP resources/read path for clients that do not surface resources as
// readable. Unknown or failing URIs are reported per item.
func (h handler) resourcesRead(ctx context.Context, _ *mcp.CallToolRequest, input ResourcesReadInput) (*mcp.CallToolResult, ResourcesReadOutput, error) {
	if err := h.approve(ctx, "resources.read", "Read resources", "Read Toby session resources", permission.RuleAllow); err != nil {
		return nil, ResourcesReadOutput{}, err
	}
	byURI := make(map[string]mcpserver.Resource, len(h.session.Resources))
	all := make([]string, 0, len(h.session.Resources))
	for _, resource := range h.session.Resources {
		byURI[resource.URI] = resource
		all = append(all, resource.URI)
	}
	wanted := input.URIs
	if len(wanted) == 0 {
		wanted = all
	}
	out := ResourcesReadOutput{Resources: make([]ReadResourceContent, 0, len(wanted))}
	failed := false
	for _, uri := range wanted {
		resource, ok := byURI[uri]
		if !ok {
			failed = true
			out.Resources = append(out.Resources, ReadResourceContent{URI: uri, Error: "unknown resource"})
			continue
		}
		text, err := resource.Read(ctx, h.session)
		if err != nil {
			failed = true
			out.Resources = append(out.Resources, ReadResourceContent{URI: uri, Error: err.Error()})
			continue
		}
		mimeType := resource.MIMEType
		if mimeType == "" {
			mimeType = "text/markdown; charset=utf-8"
		}
		out.Resources = append(out.Resources, ReadResourceContent{URI: uri, Title: resource.Title, MIMEType: mimeType, Text: text})
	}
	var result *mcp.CallToolResult
	if failed {
		result = &mcp.CallToolResult{IsError: true}
	}
	return result, out, nil
}

func (h handler) runtimeResource(context.Context) (string, error) {
	state := stateView{h.session.State}
	return markdownJSONResource("Toby Session Runtime", "Current Toby version, debug mode, sandbox runtime, and runtime paths for this session.", RuntimeResourceOutput{Version: version.String(), Debug: state.Debug, Sandbox: state.environmentSandbox(), Host: state.environmentHost()})
}

func (h handler) mcpsResource(context.Context) (string, error) {
	return markdownJSONResource("Toby Session MCPs", "Configured MCP status for this session. URLs, headers, commands, argv, and environment values are redacted.", MCPStatusOutput{Servers: stateView{h.session.State}.mcpStatusItems()})
}

func (h handler) toolsResource(context.Context) (string, error) {
	state := stateView{h.session.State}
	return markdownJSONResource("Toby Session Tools", "Active and available Toby tools plus provider summaries for this session.", ToolsResourceOutput{Tools: state.environmentTools(), Providers: state.environmentProviders()})
}

func (h handler) projectsResource(context.Context) (string, error) {
	state := stateView{h.session.State}
	return markdownJSONResource("Toby Session Projects", "Visible projects, additional binds, and managed mounts for this session.", ProjectsResourceOutput{Projects: state.environmentProjects(), Mounts: state.environmentMounts(), Binds: state.environmentBinds()})
}

func markdownJSONResource(title, description string, value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# %s\n\n%s\n\n```json\n%s\n```\n", title, description, data), nil
}

func (h handler) mcpStart(ctx context.Context, _ *mcp.CallToolRequest, input MCPNameInput) (*mcp.CallToolResult, MCPActionOutput, error) {
	return h.mcpLifecycle(ctx, "start", input.Name)
}

func (h handler) mcpStop(ctx context.Context, _ *mcp.CallToolRequest, input MCPNameInput) (*mcp.CallToolResult, MCPActionOutput, error) {
	return h.mcpLifecycle(ctx, "stop", input.Name)
}

func (h handler) mcpRestart(ctx context.Context, _ *mcp.CallToolRequest, input MCPNameInput) (*mcp.CallToolResult, MCPActionOutput, error) {
	return h.mcpLifecycle(ctx, "restart", input.Name)
}

func (h handler) mcpLifecycle(ctx context.Context, action, name string) (*mcp.CallToolResult, MCPActionOutput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, MCPActionOutput{}, fmt.Errorf("mcp name is required")
	}
	if err := h.approve(ctx, "mcp."+action, "MCP "+action, fmt.Sprintf("%s MCP server %q", action, name), permission.RuleAsk); err != nil {
		return nil, MCPActionOutput{}, err
	}
	proxy := h.session.State.MCPProxy
	if proxy == nil {
		return nil, MCPActionOutput{}, fmt.Errorf("mcp proxy service is not configured")
	}
	var err error
	switch action {
	case "start":
		err = proxy.Start(ctx, name)
	case "stop":
		err = proxy.Stop(ctx, name)
	case "restart":
		err = proxy.Restart(ctx, name)
	default:
		err = fmt.Errorf("unsupported mcp action %q", action)
	}
	if err != nil {
		return nil, MCPActionOutput{}, err
	}
	return nil, MCPActionOutput{Name: name, Action: action, Status: stateView{h.session.State}.mcpStatusItem(name)}, nil
}
