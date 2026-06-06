// Package gitservice contributes the host-backed Git tools
// (git.commit/fetch/push/rebase/tag) and the toby://docs/git resource to the Toby
// MCP server. Each tool forwards to the session's GitClient under the session lock
// so host Git operations never interleave within one session.
package gitservice

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/control/mcpserver"
)

const gitCommitDescription = "Commit staged files in a visible repository using host Git."

const gitFetchDescription = "Fetch remote refs in a visible repository using host Git."

const gitPushDescription = "Push one branch, optionally with all tags, from a visible repository using host Git."

const gitRebaseDescription = "Start, continue, or abort a rebase in a visible repository using host Git."

const gitTagDescription = "Create an annotated tag in a visible repository using host Git."

// Service contributes the host Git tools and docs resource into the MCP server.
type Service struct{}

var _ mcpserver.Service = Service{}

func (Service) Tools() []mcpserver.Tool {
	return []mcpserver.Tool{
		{Name: "git.commit", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "git.commit", Description: gitCommitDescription}, handler{session}.commit)
		}},
		{Name: "git.fetch", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "git.fetch", Description: gitFetchDescription}, handler{session}.fetch)
		}},
		{Name: "git.push", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "git.push", Description: gitPushDescription}, handler{session}.push)
		}},
		{Name: "git.rebase", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "git.rebase", Description: gitRebaseDescription}, handler{session}.rebase)
		}},
		{Name: "git.tag", Register: func(server *mcp.Server, session *mcpserver.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: "git.tag", Description: gitTagDescription}, handler{session}.tag)
		}},
	}
}

func (Service) Resources() []mcpserver.Resource {
	return []mcpserver.Resource{
		{
			URI:         "toby://docs/git",
			Name:        "toby.docs.git",
			Title:       "Toby Git",
			Description: "Guidance for using Toby host Git MCP tools.",
			FS:          resourceDocs,
			FilePath:    "resources/git.md",
		},
	}
}
