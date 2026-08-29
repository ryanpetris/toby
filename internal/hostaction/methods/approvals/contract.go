// Package approvals is the host-side action capability for one launch's
// approval records: listing pending approvals, deciding them, and waiting for
// a decision. Approving executes the held request through the launch's own
// host-action router and saves its response for the waiting caller.
package approvals

import (
	"encoding/json"
	"errors"

	"petris.dev/toby/internal/hostaction"
)

// Host-action method names for the approvals capability.
const (
	// MethodList is the pending-approvals listing RPC method.
	MethodList = "approvals.list"
	// MethodDecide is the approval decision RPC method.
	MethodDecide = "approvals.decide"
	// MethodWait is the blocking decision-wait RPC method.
	MethodWait = "approvals.wait"
)

// NewListRequest constructs a pending-approvals listing request.
func NewListRequest(id int64) ([]byte, error) {
	return hostaction.NewRequest(id, MethodList, nil)
}

// NewDecideRequest constructs an approval decision request.
func NewDecideRequest(id int64, params DecideParams) ([]byte, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return hostaction.NewRequest(id, MethodDecide, encoded)
}

// NewWaitRequest constructs a blocking decision-wait request.
func NewWaitRequest(id int64, params WaitParams) ([]byte, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return hostaction.NewRequest(id, MethodWait, encoded)
}

// DecodeDecideParams decodes and validates decision parameters.
func DecodeDecideParams(raw json.RawMessage) (DecideParams, error) {
	params, err := hostaction.DecodeParams[DecideParams](raw)
	if err != nil {
		return DecideParams{}, err
	}
	if params.ApprovalID == "" {
		return DecideParams{}, errors.New("approval_id is required")
	}
	return params, nil
}

// DecodeWaitParams decodes and validates wait parameters.
func DecodeWaitParams(raw json.RawMessage) (WaitParams, error) {
	params, err := hostaction.DecodeParams[WaitParams](raw)
	if err != nil {
		return WaitParams{}, err
	}
	if params.ApprovalID == "" {
		return WaitParams{}, errors.New("approval_id is required")
	}
	if params.TimeoutMS < 0 {
		return WaitParams{}, errors.New("timeout_ms must not be negative")
	}
	return params, nil
}

// DecodeListResult decodes an untyped RPC result as a listing result.
func DecodeListResult(result any) (ListResult, error) {
	return hostaction.DecodeResult[ListResult](result)
}

// DecodeDecideResult decodes an untyped RPC result as a decision result.
func DecodeDecideResult(result any) (DecideResult, error) {
	return hostaction.DecodeResult[DecideResult](result)
}

// DecodeWaitResult decodes an untyped RPC result as a wait result.
func DecodeWaitResult(result any) (WaitResult, error) {
	return hostaction.DecodeResult[WaitResult](result)
}
