package gitservice

// handler binds the per-session context for one Git tool invocation and forwards
// each call to the session's GitClient under the session lock.

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/git"
	"petris.dev/toby/internal/tobymcp"
)

// toolOutput is the structured tool result: either the git result of an
// executed action, or the approval the held action awaits.
type toolOutput struct {
	git.Result
	ApprovalRequired *approvalRequired `json:"approval_required,omitempty"`
}

// approvalRequired identifies the pending approval a held git action awaits.
type approvalRequired struct {
	Approval string `json:"approval" jsonschema:"approval id to pass to approvals_wait"`
	Name     string `json:"name" jsonschema:"human-readable action name"`
	Message  string `json:"message" jsonschema:"human-readable action description"`
	Command  string `json:"command" jsonschema:"command the user runs in another terminal to decide"`
}

// handler binds the per-session context for one tool invocation.
type handler struct {
	session *tobymcp.Session
}

func (h handler) commit(ctx context.Context, _ *mcp.CallToolRequest, input git.CommitParams) (*mcp.CallToolResult, toolOutput, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Commit(ctx, input) })
}

func (h handler) fetch(ctx context.Context, _ *mcp.CallToolRequest, input git.RepositoryParams) (*mcp.CallToolResult, toolOutput, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Fetch(ctx, input) })
}

func (h handler) push(ctx context.Context, _ *mcp.CallToolRequest, input git.PushParams) (*mcp.CallToolResult, toolOutput, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Push(ctx, input) })
}

func (h handler) rebase(ctx context.Context, _ *mcp.CallToolRequest, input git.RebaseParams) (*mcp.CallToolResult, toolOutput, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Rebase(ctx, input) })
}

func (h handler) tag(ctx context.Context, _ *mcp.CallToolRequest, input git.TagParams) (*mcp.CallToolResult, toolOutput, error) {
	return h.run(func() (git.Result, error) { return h.session.Git.Tag(ctx, input) })
}

// run executes a single Git call under the session lock and shapes the tool result.
func (h handler) run(call func() (git.Result, error)) (*mcp.CallToolResult, toolOutput, error) {
	if h.session == nil || h.session.Git == nil {
		return nil, toolOutput{}, fmt.Errorf(
			"live launch Git capability is unavailable",
		)
	}

	var result git.Result
	var err error
	h.session.Serialize(func() { result, err = call() })
	var pending *approvalPendingError
	if errors.As(err, &pending) {
		content, output := approvalRequiredResult(pending.data)
		return content, output, nil
	}
	if err != nil {
		return nil, toolOutput{}, err
	}
	return gitToolResult(result), toolOutput{Result: result}, nil
}

// approvalRequiredResult shapes an approval-required outcome as a non-error
// result: the action is held, not failed, and approvals_wait delivers its
// eventual result.
func approvalRequiredResult(
	data hostaction.ApprovalRequiredData,
) (*mcp.CallToolResult, toolOutput) {
	command := "toby approvals " + data.ApprovalID
	text := fmt.Sprintf(
		"Approval required: %s (%s).\n"+
			"Ask the user to run in another terminal: %s\n"+
			"Then call approvals_wait with {\"approval\":%q}; once the user "+
			"approves, the action runs on the host and approvals_wait returns "+
			"its result. Do not re-run this tool while the approval is pending.",
		data.Name,
		data.Message,
		command,
		data.ApprovalID,
	)

	return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, toolOutput{
			ApprovalRequired: &approvalRequired{
				Approval: data.ApprovalID,
				Name:     data.Name,
				Message:  data.Message,
				Command:  command,
			},
		}
}

func gitToolResult(result git.Result) *mcp.CallToolResult {
	if result.ExitCode == 0 {
		return nil
	}
	return &mcp.CallToolResult{IsError: true}
}
