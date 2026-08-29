package approvalsservice

// Implements the approvals MCP client over a live launch-owned reverse
// capability.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"time"

	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/hostaction/methods/approvals"
	"petris.dev/toby/internal/tobymcp"
)

// ReverseCaller sends one encoded host-action request over a live launch
// capability and returns its encoded response.
type ReverseCaller interface {
	// Call sends one encoded host-action request.
	Call(context.Context, json.RawMessage) (json.RawMessage, error)
}

// NewReverseApprovalsClient creates an approvals client whose authority lasts
// only as long as caller continues accepting reverse capability calls.
func NewReverseApprovalsClient(caller ReverseCaller) tobymcp.ApprovalsClient {
	return &reverseApprovalsClient{caller: caller}
}

type reverseApprovalsClient struct {
	caller ReverseCaller
}

var _ tobymcp.ApprovalsClient = (*reverseApprovalsClient)(nil)

func (c *reverseApprovalsClient) Wait(
	ctx context.Context,
	approvalID string,
	timeout time.Duration,
) (approvals.WaitResult, error) {
	if c == nil || !hasCaller(c.caller) {
		return approvals.WaitResult{}, fmt.Errorf(
			"reverse approvals caller is not configured",
		)
	}
	request, err := approvals.NewWaitRequest(1, approvals.WaitParams{
		ApprovalID: approvalID,
		TimeoutMS:  timeout.Milliseconds(),
	})
	if err != nil {
		return approvals.WaitResult{}, err
	}

	response, err := c.caller.Call(ctx, json.RawMessage(request))
	if err != nil {
		return approvals.WaitResult{}, err
	}
	decoded, err := hostaction.DecodeResponse(response)
	if err != nil {
		return approvals.WaitResult{}, err
	}
	if decoded.Error != nil {
		return approvals.WaitResult{}, decoded.Error
	}

	return approvals.DecodeWaitResult(decoded.Result)
}

func hasCaller(caller ReverseCaller) bool {
	if caller == nil {
		return false
	}

	value := reflect.ValueOf(caller)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}
