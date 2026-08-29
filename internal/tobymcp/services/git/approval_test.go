package gitservice

// Verifies that approval-required responses decode into the typed pending
// error and shape as a non-error tool result carrying the approval id.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/git"
	"petris.dev/toby/internal/tobymcp"
)

func approvalRequiredResponse(t *testing.T) []byte {
	t.Helper()
	return hostaction.ResponseError(
		[]byte("1"),
		hostaction.CodeApprovalRequired,
		"approval a1b2c3d4e5f6 required",
		hostaction.ApprovalRequiredData{
			ApprovalID: "a1b2c3d4e5f6",
			Name:       "Git push",
			Message:    "Push main to origin in repo",
		},
	)
}

func TestDecodeResponseTypesApprovalRequired(t *testing.T) {
	_, err := decodeResponse(approvalRequiredResponse(t), nil)
	var pending *approvalPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("decode error = %v, want a pending approval", err)
	}
	if pending.data.ApprovalID != "a1b2c3d4e5f6" {
		t.Fatalf("pending data = %+v", pending.data)
	}
}

type pendingGitClient struct {
	tobymcp.GitClient
	err error
}

func (c pendingGitClient) Push(context.Context, git.PushParams) (git.Result, error) {
	return git.Result{}, c.err
}

func TestPushHandlerShapesApprovalRequiredAsResult(t *testing.T) {
	_, pendingErr := decodeResponse(approvalRequiredResponse(t), nil)
	session := &tobymcp.Session{Git: pendingGitClient{err: pendingErr}}

	content, output, err := handler{session}.push(
		t.Context(),
		nil,
		git.PushParams{Repository: "repo", Branch: "main"},
	)
	if err != nil {
		t.Fatalf("push handler error = %v", err)
	}
	if content == nil || content.IsError {
		t.Fatalf("push handler content = %+v", content)
	}
	if output.ApprovalRequired == nil ||
		output.ApprovalRequired.Approval != "a1b2c3d4e5f6" ||
		output.ApprovalRequired.Command != "toby approvals a1b2c3d4e5f6" {
		t.Fatalf("push handler output = %+v", output.ApprovalRequired)
	}

	text := content.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "toby approvals a1b2c3d4e5f6") ||
		!strings.Contains(text, "approvals_wait") {
		t.Fatalf("push handler text = %q", text)
	}
}
