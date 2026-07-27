package tobymcp

// Defines the contributor contract and live reverse Git capability consumed
// by each native MCP connection.

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/hostaction/methods/git"
)

// GitClient performs approved Git operations through the live launch client.
type GitClient interface {
	// Commit creates a Git commit.
	Commit(context.Context, git.CommitParams) (git.Result, error)
	// Fetch updates remote Git refs.
	Fetch(context.Context, git.RepositoryParams) (git.Result, error)
	// Push publishes Git refs.
	Push(context.Context, git.PushParams) (git.Result, error)
	// Rebase starts, continues, or aborts a Git rebase.
	Rebase(context.Context, git.RebaseParams) (git.Result, error)
	// Tag creates an annotated Git tag.
	Tag(context.Context, git.TagParams) (git.Result, error)
}

// Contributor provides tools and resources to the sandbox MCP server.
type Contributor interface {
	// Tools returns contributed MCP tools.
	Tools() []Tool
	// Resources returns contributed MCP resources.
	Resources() []Resource
}

// Tool describes one MCP tool registration.
type Tool struct {
	Name     string
	Register func(*mcp.Server, *Session)
}

// Resource describes one MCP resource registration.
type Resource struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	FS          fs.FS
	FilePath    string
	Text        func(context.Context, *Session) (string, error)
}

func validateResource(resource Resource) error {
	if resource.URI == "" {
		return fmt.Errorf("mcp resource must define a uri")
	}
	if resource.Name == "" {
		return fmt.Errorf("mcp resource %s must define a name", resource.URI)
	}
	static := resource.FS != nil || resource.FilePath != ""
	dynamic := resource.Text != nil
	if static && dynamic {
		return fmt.Errorf("mcp resource %s must define either a static file or text function", resource.URI)
	}
	if !static && !dynamic {
		return fmt.Errorf("mcp resource %s must define a static file or text function", resource.URI)
	}
	if static && (resource.FS == nil || resource.FilePath == "") {
		return fmt.Errorf("mcp resource %s static file requires fs and path", resource.URI)
	}
	return nil
}

// Register adds the resource to an MCP server.
func (r Resource) Register(mcpServer *mcp.Server, toby *Session) {
	mimeType := r.MIMEType
	if mimeType == "" {
		mimeType = "text/markdown; charset=utf-8"
	}
	mcpServer.AddResource(&mcp.Resource{URI: r.URI, Name: r.Name, Title: r.Title, Description: r.Description, MIMEType: mimeType}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		text, err := r.Read(ctx, toby)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.URI, MIMEType: mimeType, Text: text}}}, nil
	})
}

// Read produces the resource contents for a session.
func (r Resource) Read(ctx context.Context, toby *Session) (string, error) {
	if r.Text != nil {
		return r.Text(ctx, toby)
	}
	data, err := fs.ReadFile(r.FS, r.FilePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
