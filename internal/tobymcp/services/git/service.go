// Package gitservice contributes the host-backed Git tools
// (git_commit/fetch/push/rebase/tag) and the toby://docs/git resource to the Toby
// MCP server. Each tool forwards to the session's GitClient under the session lock
// so host Git operations never interleave within one session.
package gitservice

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/tobymcp"
)

const (
	toolCommit = "git_commit"
	toolFetch  = "git_fetch"
	toolPush   = "git_push"
	toolRebase = "git_rebase"
	toolTag    = "git_tag"
)

const gitCommitDescription = "Commit staged files in a visible repository using host Git."

const gitFetchDescription = "Fetch remote refs in a visible repository using host Git."

const gitPushDescription = "Push one branch, optionally with all tags, from a visible repository using host Git."

const gitRebaseDescription = "Start, continue, or abort a rebase in a visible repository using host Git."

const gitTagDescription = "Create an annotated tag in a visible repository using host Git."

// Service contributes the host Git tools and docs resource into the MCP server.
type Service struct{}

var _ tobymcp.Contributor = Service{}

// Tools returns the Git MCP tools.
func (Service) Tools() []tobymcp.Tool {
	return []tobymcp.Tool{
		{Name: toolCommit, Register: func(server *mcp.Server, session *tobymcp.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: toolCommit, Description: gitCommitDescription}, handler{session}.commit)
		}},
		{Name: toolFetch, Register: func(server *mcp.Server, session *tobymcp.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: toolFetch, Description: gitFetchDescription}, handler{session}.fetch)
		}},
		{Name: toolPush, Register: func(server *mcp.Server, session *tobymcp.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: toolPush, Description: gitPushDescription}, handler{session}.push)
		}},
		{Name: toolRebase, Register: func(server *mcp.Server, session *tobymcp.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: toolRebase, Description: gitRebaseDescription}, handler{session}.rebase)
		}},
		{Name: toolTag, Register: func(server *mcp.Server, session *tobymcp.Session) {
			mcp.AddTool(server, &mcp.Tool{Name: toolTag, Description: gitTagDescription}, handler{session}.tag)
		}},
	}
}

// Resources returns documentation resources for the Git tools.
func (Service) Resources() []tobymcp.Resource {
	return []tobymcp.Resource{
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
