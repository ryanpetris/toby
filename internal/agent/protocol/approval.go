package protocol

// Transport-independent approval listing and decision values.

import "time"

// PendingApproval describes one undecided launch approval.
type PendingApproval struct {
	ApprovalID      string    `json:"approval_id"`
	Action          string    `json:"action"`
	Name            string    `json:"name"`
	Message         string    `json:"message"`
	Created         time.Time `json:"created"`
	LaunchSessionID string    `json:"launch_session_id"`
}

// ApprovalListResult aggregates pending approvals across connected launches.
type ApprovalListResult struct {
	Approvals           []PendingApproval `json:"approvals"`
	UnreachableSessions uint64            `json:"unreachable_sessions"`
}

// ApprovalDecision reports one recorded approval decision. Changed is false
// when the record had already been decided.
type ApprovalDecision struct {
	Approval PendingApproval `json:"approval"`
	Status   string          `json:"status"`
	Changed  bool            `json:"changed"`
}
