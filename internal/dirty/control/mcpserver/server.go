package mcpserver

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"sync"

	"petris.dev/toby/control/methods/git"
	"petris.dev/toby/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
)

type Server struct {
	client    GitClient
	state     SessionState
	resources []Resource
	mu        sync.Mutex
}

type GitClient interface {
	GitCommit(context.Context, GitCommitInput) (GitOutput, error)
	GitFetch(context.Context, GitRepositoryInput) (GitOutput, error)
	GitPush(context.Context, GitPushInput) (GitOutput, error)
	GitRebase(context.Context, GitRebaseInput) (GitOutput, error)
	GitTag(context.Context, GitTagInput) (GitOutput, error)
}

const FxServiceGroup = "toby.sandbox.mcp.services"

type Service interface {
	Tools() []Tool
	Resources() []Resource
}

type Tool struct {
	Name     string
	Register func(*mcp.Server, *Server)
}

type Resource struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	FS          fs.FS
	FilePath    string
	Text        func(context.Context, *Server) (string, error)
}

type RunnerParams struct {
	fx.In

	Services []Service `group:"toby.sandbox.mcp.services"`
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

func Module() fx.Option {
	return fx.Module(
		"mcpserver",
		fx.Provide(NewGitService, NewTobyService, NewRunner),
	)
}

type GitCommitInput = git.CommitParams
type GitRepositoryInput = git.RepositoryParams
type GitPushInput = git.PushParams
type GitRebaseInput = git.RebaseParams
type GitTagInput = git.TagParams
type GitOutput = git.Result

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

const gitCommitDescription = "Commit staged files in a visible repository using host Git."

const gitFetchDescription = "Fetch remote refs in a visible repository using host Git."

const gitPushDescription = "Push one branch, optionally with all tags, from a visible repository using host Git."

const gitRebaseDescription = "Start, continue, or abort a rebase in a visible repository using host Git."

const gitTagDescription = "Create an annotated tag in a visible repository using host Git."

func (r *Runner) Handler(client GitClient, state SessionState) http.Handler {
	server := &Server{client: client, state: state.Clone(), resources: r.resources}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return r.server(server)
	}, nil)
}

func (r *Runner) server(server *Server) *mcp.Server {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "toby", Version: version.String()}, &mcp.ServerOptions{
		Instructions: serverInstructions,
	})
	for _, tool := range r.tools {
		tool.Register(mcpServer, server)
	}
	for _, resource := range r.resources {
		resource.Register(mcpServer, server)
	}
	return mcpServer
}

func (r Resource) Register(mcpServer *mcp.Server, toby *Server) {
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

func (r Resource) Read(ctx context.Context, toby *Server) (string, error) {
	if r.Text != nil {
		return r.Text(ctx, toby)
	}
	data, err := fs.ReadFile(r.FS, r.FilePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Server) gitCommit(ctx context.Context, _ *mcp.CallToolRequest, input GitCommitInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitCommit(ctx, input)
	if err != nil {
		return nil, GitOutput{}, err
	}
	return gitToolResult(result), GitOutput(result), nil
}

func (s *Server) gitFetch(ctx context.Context, _ *mcp.CallToolRequest, input GitRepositoryInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitFetch(ctx, input)
	if err != nil {
		return nil, GitOutput{}, err
	}
	return gitToolResult(result), GitOutput(result), nil
}

func (s *Server) gitPush(ctx context.Context, _ *mcp.CallToolRequest, input GitPushInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitPush(ctx, input)
	if err != nil {
		return nil, GitOutput{}, err
	}
	return gitToolResult(result), GitOutput(result), nil
}

func (s *Server) gitRebase(ctx context.Context, _ *mcp.CallToolRequest, input GitRebaseInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitRebase(ctx, input)
	if err != nil {
		return nil, GitOutput{}, err
	}
	return gitToolResult(result), GitOutput(result), nil
}

func (s *Server) gitTag(ctx context.Context, _ *mcp.CallToolRequest, input GitTagInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitTag(ctx, input)
	if err != nil {
		return nil, GitOutput{}, err
	}
	return gitToolResult(result), GitOutput(result), nil
}

func gitToolResult(result git.Result) *mcp.CallToolResult {
	if result.ExitCode == 0 {
		return nil
	}
	return &mcp.CallToolResult{IsError: true}
}
