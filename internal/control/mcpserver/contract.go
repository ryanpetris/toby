package mcpserver

// The service-plugin contract: a Service contributes Tools and Resources, a Tool
// registers an MCP tool against a session, and a Resource is served either from an
// embedded file or a dynamic text function. GitClient is the host-Git dependency a
// session exposes to Git tools.

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/control/methods/git"
)

type GitClient interface {
	Commit(context.Context, git.CommitParams) (git.Result, error)
	Fetch(context.Context, git.RepositoryParams) (git.Result, error)
	Push(context.Context, git.PushParams) (git.Result, error)
	Rebase(context.Context, git.RebaseParams) (git.Result, error)
	Tag(context.Context, git.TagParams) (git.Result, error)
}

type Service interface {
	Tools() []Tool
	Resources() []Resource
}

type Tool struct {
	Name     string
	Register func(*mcp.Server, *Session)
}

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
