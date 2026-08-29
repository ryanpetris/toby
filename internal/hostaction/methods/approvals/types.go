package approvals

// Wire parameter and result shapes for the approvals.* methods.

import "encoding/json"

// Decision status values shared by decide and wait results.
const (
	// StatusPending reports an undecided approval.
	StatusPending = "pending"
	// StatusApproved reports an approved approval.
	StatusApproved = "approved"
	// StatusDenied reports a denied approval.
	StatusDenied = "denied"
)

// PendingApproval describes one undecided approval record.
type PendingApproval struct {
	ApprovalID    string `json:"approval_id"`
	Action        string `json:"action"`
	Name          string `json:"name"`
	Message       string `json:"message"`
	CreatedUnixMS int64  `json:"created_unix_ms"`
}

// ListResult carries the pending approvals of one launch.
type ListResult struct {
	Approvals []PendingApproval `json:"approvals"`
}

// DecideParams selects one approval and the decision to record.
type DecideParams struct {
	ApprovalID string `json:"approval_id"`
	Approve    bool   `json:"approve"`
}

// DecideResult reports the decision outcome. Changed is false when the record
// was already decided.
type DecideResult struct {
	ApprovalID string `json:"approval_id"`
	Status     string `json:"status"`
	Changed    bool   `json:"changed"`
	Name       string `json:"name"`
	Message    string `json:"message"`
}

// WaitParams selects one approval to wait on and an optional timeout.
type WaitParams struct {
	ApprovalID string `json:"approval_id"`
	TimeoutMS  int64  `json:"timeout_ms,omitempty"`
}

// WaitResult reports the awaited decision. Action names the held method and
// Response carries the executed request's complete JSON-RPC response once the
// approval completed.
type WaitResult struct {
	ApprovalID string          `json:"approval_id"`
	Status     string          `json:"status"`
	Action     string          `json:"action,omitempty"`
	Response   json.RawMessage `json:"response,omitempty"`
}
