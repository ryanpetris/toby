package mcpserver

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/fx"
)

type GitServiceResult struct {
	fx.Out

	Service Service `group:"toby.sandbox.mcp.services"`
}

type GitService struct{}

func NewGitService() GitServiceResult {
	return GitServiceResult{Service: GitService{}}
}

func (GitService) Tools() []Tool {
	return []Tool{
		{
			Name: "git.commit",
			Register: func(server *mcp.Server, toby *Server) {
				mcp.AddTool(server, &mcp.Tool{Name: "git.commit", Description: gitCommitDescription}, toby.gitCommit)
			},
		},
		{
			Name: "git.fetch",
			Register: func(server *mcp.Server, toby *Server) {
				mcp.AddTool(server, &mcp.Tool{Name: "git.fetch", Description: gitFetchDescription}, toby.gitFetch)
			},
		},
		{
			Name: "git.push",
			Register: func(server *mcp.Server, toby *Server) {
				mcp.AddTool(server, &mcp.Tool{Name: "git.push", Description: gitPushDescription}, toby.gitPush)
			},
		},
		{
			Name: "git.rebase",
			Register: func(server *mcp.Server, toby *Server) {
				mcp.AddTool(server, &mcp.Tool{Name: "git.rebase", Description: gitRebaseDescription}, toby.gitRebase)
			},
		},
		{
			Name: "git.tag",
			Register: func(server *mcp.Server, toby *Server) {
				mcp.AddTool(server, &mcp.Tool{Name: "git.tag", Description: gitTagDescription}, toby.gitTag)
			},
		},
	}
}
