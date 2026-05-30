package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
)

type Server struct {
	client GitClient
	mu     sync.Mutex
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
}

type Tool struct {
	Name     string
	Register func(*mcp.Server, *Server)
}

type RunnerParams struct {
	fx.In

	Services []Service `group:"toby.sandbox.mcp.services"`
}

type Runner struct {
	tools []Tool
}

func NewRunner(params RunnerParams) (*Runner, error) {
	seen := map[string]bool{}
	var tools []Tool
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
			if seen[tool.Name] {
				return nil, fmt.Errorf("duplicate mcp tool: %s", tool.Name)
			}
			seen[tool.Name] = true
			tools = append(tools, tool)
		}
	}
	return &Runner{tools: tools}, nil
}

func Module() fx.Option {
	return fx.Module(
		"mcpserver",
		fx.Provide(NewGitService, NewRunner),
	)
}

type GitCommitInput = control.GitCommitParams
type GitRepositoryInput = control.GitRepositoryParams
type GitPushInput = control.GitPushParams
type GitRebaseInput = control.GitRebaseParams
type GitTagInput = control.GitTagParams
type GitOutput = control.GitResult

const gitServerInstructions = "Toby MCP tools: git.commit, git.fetch, git.push, git.rebase, and git.tag run host Git for repositories visible in the sandbox."

const gitCommitDescription = "Commit staged files in a visible repository using host Git."

const gitFetchDescription = "Fetch remote refs in a visible repository using host Git."

const gitPushDescription = "Push one branch, optionally with all tags, from a visible repository using host Git."

const gitRebaseDescription = "Start, continue, or abort a rebase in a visible repository using host Git."

const gitTagDescription = "Create an annotated tag in a visible repository using host Git."

func (r *Runner) Handler(client GitClient) http.Handler {
	server := &Server{client: client}
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return r.server(server)
	}, nil)
}

func (r *Runner) server(server *Server) *mcp.Server {
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "toby", Version: version.String()}, &mcp.ServerOptions{
		Instructions: gitServerInstructions,
	})
	for _, tool := range r.tools {
		tool.Register(mcpServer, server)
	}
	return mcpServer
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

func gitToolResult(result control.GitResult) *mcp.CallToolResult {
	if result.ExitCode == 0 {
		return nil
	}
	return &mcp.CallToolResult{IsError: true}
}
