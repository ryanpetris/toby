package mcpserver

import (
	"context"
	"fmt"
	"os"
	"sync"

	"petris.dev/toby/internal/control"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	client *control.Client
	mu     sync.Mutex
}

type GitCommitInput = control.GitCommitParams
type GitRepositoryInput = control.GitRepositoryParams
type GitPushInput = control.GitPushParams
type GitOutput = control.GitResult

const gitServerInstructions = "Toby MCP tools: git_commit, git_fetch, and git_push run host Git for repositories visible in the sandbox."

const gitCommitDescription = "Commit staged files in a visible repository using host Git."

const gitFetchDescription = "Fetch remote refs in a visible repository using host Git."

const gitPushDescription = "Push one branch from a visible repository using host Git."

func Run(ctx context.Context, controlPath string) error {
	if controlPath == "" {
		var err error
		controlPath, err = control.DefaultSocketPath()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(controlPath); err != nil {
		return fmt.Errorf("toby-sandbox mcp must run inside a Toby sandbox: %s is not available", controlPath)
	}

	server := &Server{client: control.NewClient(controlPath)}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "toby", Version: "dev"}, &mcp.ServerOptions{
		Instructions: gitServerInstructions,
	})
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "git_commit",
		Description: gitCommitDescription,
	}, server.gitCommit)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "git_fetch",
		Description: gitFetchDescription,
	}, server.gitFetch)
	mcp.AddTool(mcpServer, &mcp.Tool{
		Name:        "git_push",
		Description: gitPushDescription,
	}, server.gitPush)
	return mcpServer.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) gitCommit(ctx context.Context, _ *mcp.CallToolRequest, input GitCommitInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitCommit(input.Repository, input.Message)
	if err != nil {
		return nil, GitOutput{}, err
	}
	return gitToolResult(result), GitOutput(result), nil
}

func (s *Server) gitFetch(ctx context.Context, _ *mcp.CallToolRequest, input GitRepositoryInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitFetch(input.Repository)
	if err != nil {
		return nil, GitOutput{}, err
	}
	return gitToolResult(result), GitOutput(result), nil
}

func (s *Server) gitPush(ctx context.Context, _ *mcp.CallToolRequest, input GitPushInput) (*mcp.CallToolResult, GitOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.client.GitPush(input.Repository, input.Branch, input.Origin)
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
