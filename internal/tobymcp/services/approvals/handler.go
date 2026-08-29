package approvalsservice

// handler waits on one approval decision and renders the executed action's
// saved response as the result the original tool call would have produced.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/approvals"
	"petris.dev/toby/internal/hostaction/methods/git"
	"petris.dev/toby/internal/tobymcp"
	gitservice "petris.dev/toby/internal/tobymcp/services/git"
)

const (
	defaultWaitSeconds = 300
	maxWaitSeconds     = 1800
)

// waitInput selects the approval to wait on.
type waitInput struct {
	Approval       string `json:"approval" jsonschema:"approval id from an approval-required tool result"`
	TimeoutSeconds int64  `json:"timeout_seconds,omitempty" jsonschema:"maximum seconds to wait before returning status pending, default 300, max 1800"`
}

// waitOutput reports the decision. Result carries the executed git action's
// result; Response carries the raw result of any other action.
type waitOutput struct {
	Approval string          `json:"approval"`
	Status   string          `json:"status" jsonschema:"approved, denied, or pending"`
	Result   *git.Result     `json:"result,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

// handler binds the per-session context for one wait invocation.
type handler struct {
	session *tobymcp.Session
}

// wait blocks on the approval decision. It deliberately does not take the
// session lock: a blocked wait inside it would deadlock every other tool call
// on this connection.
func (h handler) wait(ctx context.Context, _ *mcp.CallToolRequest, input waitInput) (*mcp.CallToolResult, waitOutput, error) {
	if h.session == nil || h.session.Approvals == nil {
		return nil, waitOutput{}, fmt.Errorf(
			"live launch approvals capability is unavailable",
		)
	}
	if input.Approval == "" {
		return nil, waitOutput{}, fmt.Errorf("approval id is required")
	}

	seconds := input.TimeoutSeconds
	switch {
	case seconds <= 0:
		seconds = defaultWaitSeconds
	case seconds > maxWaitSeconds:
		seconds = maxWaitSeconds
	}
	result, err := h.session.Approvals.Wait(
		ctx,
		input.Approval,
		time.Duration(seconds)*time.Second,
	)
	if err != nil {
		var rpcErr *hostaction.RPCError
		if errors.As(err, &rpcErr) &&
			rpcErr.Code == hostaction.CodeApprovalNotFound {
			return nil, waitOutput{}, fmt.Errorf(
				"unknown approval %s: the launch that created it may have "+
					"ended; re-run the original tool to create a fresh approval",
				input.Approval,
			)
		}
		return nil, waitOutput{}, err
	}

	switch result.Status {
	case approvals.StatusDenied:
		return textResult(fmt.Sprintf(
				"Approval %s was denied by the user. Do not retry this action "+
					"without new instructions.",
				input.Approval,
			)),
			waitOutput{Approval: input.Approval, Status: result.Status},
			nil
	case approvals.StatusPending:
		return textResult(fmt.Sprintf(
				"Approval %s is still pending after %ds. Remind the user to "+
					"run: toby approvals %s - then call approvals_wait again.",
				input.Approval,
				seconds,
				input.Approval,
			)),
			waitOutput{Approval: input.Approval, Status: result.Status},
			nil
	}

	return h.approvedResult(input.Approval, result)
}

// approvedResult renders the executed action's saved response the way the
// original tool call would have reported it.
func (h handler) approvedResult(
	approvalID string,
	result approvals.WaitResult,
) (*mcp.CallToolResult, waitOutput, error) {
	if strings.HasPrefix(result.Action, "git.") {
		gitResult, err := gitservice.DecodeToolResponse(result.Response)
		if err != nil {
			return nil, waitOutput{}, err
		}
		text := fmt.Sprintf(
			"Approval %s approved; %s completed with exit code %d.",
			approvalID,
			result.Action,
			gitResult.ExitCode,
		)
		if gitResult.Stdout != "" {
			text += "\nstdout:\n" + gitResult.Stdout
		}
		if gitResult.Stderr != "" {
			text += "\nstderr:\n" + gitResult.Stderr
		}
		content := textResult(text)
		content.IsError = gitResult.ExitCode != 0
		return content, waitOutput{
			Approval: approvalID,
			Status:   result.Status,
			Result:   &gitResult,
		}, nil
	}

	decoded, err := hostaction.DecodeResponse(result.Response)
	if err != nil {
		return nil, waitOutput{}, fmt.Errorf(
			"decode approved %s response: %w",
			result.Action,
			err,
		)
	}
	if decoded.Error != nil {
		return nil, waitOutput{}, decoded.Error
	}
	raw, err := json.Marshal(decoded.Result)
	if err != nil {
		return nil, waitOutput{}, fmt.Errorf(
			"encode approved %s result: %w",
			result.Action,
			err,
		)
	}
	return textResult(fmt.Sprintf(
			"Approval %s approved; %s completed:\n%s",
			approvalID,
			result.Action,
			raw,
		)),
		waitOutput{
			Approval: approvalID,
			Status:   result.Status,
			Response: raw,
		},
		nil
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}
