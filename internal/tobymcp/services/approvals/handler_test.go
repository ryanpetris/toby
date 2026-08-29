package approvalsservice

// Verifies the wait tool's rendering of decisions and saved responses.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/approvals"
	"petris.dev/toby/internal/hostaction/methods/git"
	"petris.dev/toby/internal/tobymcp"
)

type fakeApprovalsClient struct {
	result  approvals.WaitResult
	err     error
	timeout time.Duration
}

func (c *fakeApprovalsClient) Wait(
	_ context.Context,
	_ string,
	timeout time.Duration,
) (approvals.WaitResult, error) {
	c.timeout = timeout
	return c.result, c.err
}

func waitWith(
	t *testing.T,
	client *fakeApprovalsClient,
	input waitInput,
) (*mcp.CallToolResult, waitOutput, error) {
	t.Helper()

	session := &tobymcp.Session{Approvals: client}
	return handler{session}.wait(t.Context(), nil, input)
}

func TestWaitReturnsExecutedGitResult(t *testing.T) {
	response := hostaction.ResponseOK([]byte("1"), git.Result{
		Repository: "repo",
		ExitCode:   0,
		Stdout:     "pushed\n",
	})
	client := &fakeApprovalsClient{result: approvals.WaitResult{
		ApprovalID: "a1b2c3d4e5f6",
		Status:     approvals.StatusApproved,
		Action:     "git.push",
		Response:   response,
	}}

	content, output, err := waitWith(t, client, waitInput{Approval: "a1b2c3d4e5f6"})
	if err != nil {
		t.Fatalf("wait error = %v", err)
	}
	if content == nil || content.IsError {
		t.Fatalf("wait content = %+v", content)
	}
	if output.Status != approvals.StatusApproved ||
		output.Result == nil ||
		output.Result.Repository != "repo" {
		t.Fatalf("wait output = %+v", output)
	}
	text := content.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "git.push completed with exit code 0") ||
		!strings.Contains(text, "pushed") {
		t.Fatalf("wait text = %q", text)
	}
	if client.timeout != defaultWaitSeconds*time.Second {
		t.Fatalf("wait timeout = %v", client.timeout)
	}
}

func TestWaitMarksFailedGitResultAsError(t *testing.T) {
	response := hostaction.ResponseOK([]byte("1"), git.Result{
		Repository: "repo",
		ExitCode:   1,
		Stderr:     "rejected\n",
	})
	client := &fakeApprovalsClient{result: approvals.WaitResult{
		ApprovalID: "a1b2c3d4e5f6",
		Status:     approvals.StatusApproved,
		Action:     "git.push",
		Response:   response,
	}}

	content, output, err := waitWith(t, client, waitInput{Approval: "a1b2c3d4e5f6"})
	if err != nil {
		t.Fatalf("wait error = %v", err)
	}
	if content == nil || !content.IsError {
		t.Fatalf("wait content = %+v", content)
	}
	if output.Result == nil || output.Result.ExitCode != 1 {
		t.Fatalf("wait output = %+v", output)
	}
}

func TestWaitReportsDeniedAndPending(t *testing.T) {
	denied := &fakeApprovalsClient{result: approvals.WaitResult{
		ApprovalID: "a1b2c3d4e5f6",
		Status:     approvals.StatusDenied,
	}}
	content, output, err := waitWith(t, denied, waitInput{Approval: "a1b2c3d4e5f6"})
	if err != nil || content == nil || content.IsError {
		t.Fatalf("denied wait = (%+v, %v)", content, err)
	}
	if output.Status != approvals.StatusDenied {
		t.Fatalf("denied output = %+v", output)
	}

	pending := &fakeApprovalsClient{result: approvals.WaitResult{
		ApprovalID: "a1b2c3d4e5f6",
		Status:     approvals.StatusPending,
	}}
	content, output, err = waitWith(
		t,
		pending,
		waitInput{Approval: "a1b2c3d4e5f6", TimeoutSeconds: 9000},
	)
	if err != nil || content == nil || content.IsError {
		t.Fatalf("pending wait = (%+v, %v)", content, err)
	}
	if output.Status != approvals.StatusPending {
		t.Fatalf("pending output = %+v", output)
	}
	text := content.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "toby approvals a1b2c3d4e5f6") {
		t.Fatalf("pending text = %q", text)
	}
	if pending.timeout != maxWaitSeconds*time.Second {
		t.Fatalf("pending timeout = %v, want the cap", pending.timeout)
	}
}

func TestWaitTranslatesUnknownApprovals(t *testing.T) {
	client := &fakeApprovalsClient{err: &hostaction.RPCError{
		Code:    hostaction.CodeApprovalNotFound,
		Message: "approval not found",
	}}

	_, _, err := waitWith(t, client, waitInput{Approval: "a1b2c3d4e5f6"})
	if err == nil || !strings.Contains(err.Error(), "unknown approval") {
		t.Fatalf("unknown wait error = %v", err)
	}
}
