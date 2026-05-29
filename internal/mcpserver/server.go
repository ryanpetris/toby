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

type ProjectListInput struct{}

type ProjectInput struct {
	Name string `json:"name" jsonschema:"project directory name under XDG_PROJECTS_DIR"`
}

type ProjectListOutput = control.ProjectListResult
type ProjectReadmeOutput = control.ProjectReadmeResult
type MountOutput = control.MountResult
type GitCommitInput = control.GitCommitParams
type GitRepositoryInput = control.GitRepositoryParams
type GitPushInput = control.GitPushParams
type GitOutput = control.GitResult

const gitServerInstructions = "Toby MCP tools: git_commit, git_fetch, and git_push run host Git for repositories visible in the sandbox."

const projectServerInstructions = " project_list lists projects, project_readme reads a project's README.md, and project_mount requests access to a project directory."

const projectListDescription = "List available project directories."

const projectReadmeDescription = "Read a project's README.md without mounting the project."

const projectMountDescription = "Request access to a project directory."

const gitCommitDescription = "Commit staged files in a visible repository using host Git."

const gitFetchDescription = "Fetch remote refs in a visible repository using host Git."

const gitPushDescription = "Push one branch from a visible repository using host Git."

func Run(ctx context.Context, controlPath string) error {
	if controlPath == "" {
		var err error
		controlPath, err = control.DefaultControlPath()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(controlPath); err != nil {
		return fmt.Errorf("toby mcp must run inside a Toby sandbox: %s is not available", controlPath)
	}

	server := &Server{client: control.NewClient(controlPath)}
	mountableProjects := mountableProjectsEnabled()
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "toby", Version: "dev"}, &mcp.ServerOptions{
		Instructions: serverInstructions(mountableProjects),
	})
	if mountableProjects {
		mcp.AddTool(mcpServer, &mcp.Tool{
			Name:        "project_list",
			Description: projectListDescription,
		}, server.projectList)
		mcp.AddTool(mcpServer, &mcp.Tool{
			Name:        "project_readme",
			Description: projectReadmeDescription,
		}, server.projectReadme)
		mcp.AddTool(mcpServer, &mcp.Tool{
			Name:        "project_mount",
			Description: projectMountDescription,
		}, server.projectMount)
	}
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

func (s *Server) projectList(ctx context.Context, _ *mcp.CallToolRequest, input ProjectListInput) (*mcp.CallToolResult, ProjectListOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	projects, err := s.client.ProjectList()
	if err != nil {
		return nil, ProjectListOutput{}, err
	}
	return nil, projects, nil
}

func (s *Server) projectReadme(ctx context.Context, _ *mcp.CallToolRequest, input ProjectInput) (*mcp.CallToolResult, ProjectReadmeOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	readme, err := s.client.ProjectReadme(input.Name)
	if err != nil {
		return nil, ProjectReadmeOutput{}, err
	}
	return nil, readme, nil
}

func (s *Server) projectMount(ctx context.Context, _ *mcp.CallToolRequest, input ProjectInput) (*mcp.CallToolResult, MountOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mount, err := s.client.ProjectMount(input.Name)
	if err != nil {
		return nil, MountOutput{}, err
	}
	return nil, MountOutput(mount), nil
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

func mountableProjectsEnabled() bool {
	return os.Getenv("TOBY_MOUNTABLE_PROJECTS") == "1"
}

func serverInstructions(mountableProjects bool) string {
	if mountableProjects {
		return gitServerInstructions + projectServerInstructions
	}
	return gitServerInstructions
}
