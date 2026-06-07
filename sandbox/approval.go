package sandbox

// Approval contract: before a privileged host operation runs on the tool's behalf,
// the user may be asked to approve or deny it. The interactive sandbox runtime owns
// the terminal and implements ApprovalPrompter (it shows a modal); host-side services
// request a decision through it.

import "context"

// ApprovalRequest describes an action awaiting the user's decision.
type ApprovalRequest struct {
	Action  string // the action id, matching its RPC method name, e.g. "git.commit"
	Name    string // a human label, e.g. "Git commit"
	Message string // a one-line description of what will happen
}

// ApprovalPrompter shows an approval UI and blocks for the user's decision, reporting
// whether the action is allowed.
type ApprovalPrompter interface {
	PromptApproval(ctx context.Context, req ApprovalRequest) (allow bool, err error)
}
