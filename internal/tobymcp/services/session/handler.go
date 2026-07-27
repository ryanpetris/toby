package sessionservice

// Binds one native session to resources.read and the toby://session/*
// introspection resources.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/tobymcp"
	"petris.dev/toby/internal/version"
)

// handler binds the per-session context for one tool or resource invocation.
type handler struct {
	session *tobymcp.Session
}

// resourcesRead returns the contents of the named toby:// resources, mirroring
// the MCP resources/read path for clients that do not surface resources as
// readable. Unknown or failing URIs are reported per item.
func (h handler) resourcesRead(ctx context.Context, _ *mcp.CallToolRequest, input ResourcesReadInput) (*mcp.CallToolResult, ResourcesReadOutput, error) {
	byURI := make(map[string]tobymcp.Resource, len(h.session.Resources))
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
	state := stateView{h.session.Snapshot}
	output := RuntimeResourceOutput{
		Version: version.String(),
		Debug:   state.Debug,
		Sandbox: state.environmentSandbox(),
	}

	return markdownJSONResource(
		"Toby Session Runtime",
		"Current Toby version, debug mode, sandbox runtime, and runtime paths for this session.",
		output,
	)
}

func (h handler) mcpsResource(context.Context) (string, error) {
	output := MCPStatusOutput{
		Servers: stateView{h.session.Snapshot}.mcpStatusItems(),
	}

	return markdownJSONResource(
		"Toby Session MCPs",
		"Configured MCP status for this session. URLs, headers, commands, argv, environment values, credentials, and host paths are excluded.",
		output,
	)
}

func (h handler) toolsResource(context.Context) (string, error) {
	state := stateView{h.session.Snapshot}
	output := ToolsResourceOutput{
		Tools:  state.environmentTools(),
		Models: state.environmentModels(),
	}

	return markdownJSONResource(
		"Toby Session Tools",
		"Active and available Toby tools plus models endpoint summaries for this session.",
		output,
	)
}

func (h handler) projectsResource(context.Context) (string, error) {
	state := stateView{h.session.Snapshot}
	output := ProjectsResourceOutput{
		Projects: state.environmentProjects(),
		Mounts:   state.environmentMounts(),
		Binds:    state.environmentBinds(),
	}

	return markdownJSONResource(
		"Toby Session Projects",
		"Visible projects, additional binds, and managed mounts for this session.",
		output,
	)
}

func markdownJSONResource(title, description string, value any) (string, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("# %s\n\n%s\n\n```json\n%s\n```\n", title, description, data), nil
}
