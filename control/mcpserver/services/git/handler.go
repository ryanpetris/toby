package gitservice

// handler binds the per-session context for one Git tool invocation and forwards
// each call to the session's GitClient under the session lock.

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/control/mcpserver"
	"petris.dev/toby/control/methods/git"
)

// handler binds the per-session context for one tool invocation.
type handler struct {
	session *mcpserver.Session
}

func (h handler) commit(ctx context.Context, _ *mcp.CallToolRequest, input git.CommitParams) (*mcp.CallToolResult, git.Result, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Commit(ctx, input) })
}

func (h handler) fetch(ctx context.Context, _ *mcp.CallToolRequest, input git.RepositoryParams) (*mcp.CallToolResult, git.Result, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Fetch(ctx, input) })
}

func (h handler) push(ctx context.Context, _ *mcp.CallToolRequest, input git.PushParams) (*mcp.CallToolResult, git.Result, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Push(ctx, input) })
}

func (h handler) rebase(ctx context.Context, _ *mcp.CallToolRequest, input git.RebaseParams) (*mcp.CallToolResult, git.Result, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Rebase(ctx, input) })
}

func (h handler) tag(ctx context.Context, _ *mcp.CallToolRequest, input git.TagParams) (*mcp.CallToolResult, git.Result, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Tag(ctx, input) })
}

// run executes a single Git call under the session lock and shapes the tool result.
func (h handler) run(call func() (git.Result, error)) (*mcp.CallToolResult, git.Result, error) {
	var result git.Result
	var err error
	h.session.Serialize(func() { result, err = call() })
	if err != nil {
		return nil, git.Result{}, err
	}
	return gitToolResult(result), result, nil
}

func gitToolResult(result git.Result) *mcp.CallToolResult {
	if result.ExitCode == 0 {
		return nil
	}
	return &mcp.CallToolResult{IsError: true}
}
