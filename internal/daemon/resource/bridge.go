package resource

// StdioBridge: fronts a stdio MCP server with a streamable-HTTP MCP server,
// connecting to the sidecar's stdin/stdout as an upstream client and mirroring
// its tools, prompts, and resources (re-syncing on list-changed notifications).

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"petris.dev/toby/internal/version"
)

type StdioBridge struct {
	name   string
	server *mcp.Server

	mu                sync.RWMutex
	upstream          *mcp.ClientSession
	tools             []string
	resources         []string
	resourceTemplates []string
	prompts           []string
}

func NewStdioBridge(name string) *StdioBridge {
	bridge := &StdioBridge{name: name}
	bridge.server = mcp.NewServer(&mcp.Implementation{Name: "toby-mcp-" + name, Version: version.String()}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{
			Tools:     &mcp.ToolCapabilities{ListChanged: true},
			Prompts:   &mcp.PromptCapabilities{ListChanged: true},
			Resources: &mcp.ResourceCapabilities{ListChanged: true},
		},
	})
	return bridge
}

func (b *StdioBridge) Handler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return b.server }, nil)
}

func (b *StdioBridge) Attach(ctx context.Context, handle *ProcessHandle) error {
	if handle == nil || handle.Stdout() == nil || handle.Stdin() == nil {
		return fmt.Errorf("stdio MCP process did not expose stdin/stdout")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "toby", Version: version.String()}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			go b.SyncFeatures(context.Background())
		},
		PromptListChangedHandler: func(context.Context, *mcp.PromptListChangedRequest) {
			go b.SyncFeatures(context.Background())
		},
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) {
			go b.SyncFeatures(context.Background())
		},
	})
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: handle.Stdout(), Writer: handle.Stdin()}, nil)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.upstream = session
	b.mu.Unlock()
	return b.SyncFeatures(ctx)
}

func (b *StdioBridge) SyncFeatures(ctx context.Context) error {
	upstream := b.session()
	if upstream == nil {
		return fmt.Errorf("stdio MCP upstream is not connected")
	}
	capabilities := upstream.InitializeResult().Capabilities
	if capabilities != nil && capabilities.Tools != nil {
		if err := b.syncTools(ctx, upstream); err != nil {
			return err
		}
	} else {
		b.clearTools()
	}
	if capabilities != nil && capabilities.Prompts != nil {
		if err := b.syncPrompts(ctx, upstream); err != nil {
			return err
		}
	} else {
		b.clearPrompts()
	}
	if capabilities != nil && capabilities.Resources != nil {
		if err := b.syncResources(ctx, upstream); err != nil {
			return err
		}
		return b.syncResourceTemplates(ctx, upstream)
	}
	b.clearResources()
	return nil
}

func (b *StdioBridge) clearTools() {
	b.mu.Lock()
	old := append([]string(nil), b.tools...)
	b.tools = nil
	b.mu.Unlock()
	b.server.RemoveTools(old...)
}

func (b *StdioBridge) clearPrompts() {
	b.mu.Lock()
	old := append([]string(nil), b.prompts...)
	b.prompts = nil
	b.mu.Unlock()
	b.server.RemovePrompts(old...)
}

func (b *StdioBridge) clearResources() {
	b.mu.Lock()
	resources := append([]string(nil), b.resources...)
	templates := append([]string(nil), b.resourceTemplates...)
	b.resources = nil
	b.resourceTemplates = nil
	b.mu.Unlock()
	b.server.RemoveResources(resources...)
	b.server.RemoveResourceTemplates(templates...)
}

func (b *StdioBridge) syncTools(ctx context.Context, upstream *mcp.ClientSession) error {
	var tools []*mcp.Tool
	for tool, err := range upstream.Tools(ctx, nil) {
		if err != nil {
			return err
		}
		tools = append(tools, tool)
	}
	b.mu.Lock()
	old := append([]string(nil), b.tools...)
	b.tools = nil
	b.mu.Unlock()
	b.server.RemoveTools(old...)
	for _, item := range tools {
		tool := *item
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{"type": "object"}
		}
		name := tool.Name
		b.server.AddTool(&tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			upstream := b.session()
			if upstream == nil {
				return nil, fmt.Errorf("stdio MCP upstream is not connected")
			}
			return upstream.CallTool(ctx, &mcp.CallToolParams{Name: req.Params.Name, Arguments: req.Params.Arguments})
		})
		b.mu.Lock()
		b.tools = append(b.tools, name)
		b.mu.Unlock()
	}
	return nil
}

func (b *StdioBridge) syncPrompts(ctx context.Context, upstream *mcp.ClientSession) error {
	var prompts []*mcp.Prompt
	for prompt, err := range upstream.Prompts(ctx, nil) {
		if err != nil {
			return err
		}
		prompts = append(prompts, prompt)
	}
	b.mu.Lock()
	old := append([]string(nil), b.prompts...)
	b.prompts = nil
	b.mu.Unlock()
	b.server.RemovePrompts(old...)
	for _, item := range prompts {
		prompt := *item
		name := prompt.Name
		b.server.AddPrompt(&prompt, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			upstream := b.session()
			if upstream == nil {
				return nil, fmt.Errorf("stdio MCP upstream is not connected")
			}
			return upstream.GetPrompt(ctx, req.Params)
		})
		b.mu.Lock()
		b.prompts = append(b.prompts, name)
		b.mu.Unlock()
	}
	return nil
}

func (b *StdioBridge) syncResources(ctx context.Context, upstream *mcp.ClientSession) error {
	var resources []*mcp.Resource
	for resource, err := range upstream.Resources(ctx, nil) {
		if err != nil {
			return err
		}
		resources = append(resources, resource)
	}
	b.mu.Lock()
	old := append([]string(nil), b.resources...)
	b.resources = nil
	b.mu.Unlock()
	b.server.RemoveResources(old...)
	for _, item := range resources {
		resource := *item
		uri := resource.URI
		b.server.AddResource(&resource, b.readResource)
		b.mu.Lock()
		b.resources = append(b.resources, uri)
		b.mu.Unlock()
	}
	return nil
}

func (b *StdioBridge) syncResourceTemplates(ctx context.Context, upstream *mcp.ClientSession) error {
	var templates []*mcp.ResourceTemplate
	for template, err := range upstream.ResourceTemplates(ctx, nil) {
		if err != nil {
			return err
		}
		templates = append(templates, template)
	}
	b.mu.Lock()
	old := append([]string(nil), b.resourceTemplates...)
	b.resourceTemplates = nil
	b.mu.Unlock()
	b.server.RemoveResourceTemplates(old...)
	for _, item := range templates {
		template := *item
		uriTemplate := template.URITemplate
		b.server.AddResourceTemplate(&template, b.readResource)
		b.mu.Lock()
		b.resourceTemplates = append(b.resourceTemplates, uriTemplate)
		b.mu.Unlock()
	}
	return nil
}

func (b *StdioBridge) readResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	upstream := b.session()
	if upstream == nil {
		return nil, fmt.Errorf("stdio MCP upstream is not connected")
	}
	return upstream.ReadResource(ctx, req.Params)
}

func (b *StdioBridge) session() *mcp.ClientSession {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.upstream
}
