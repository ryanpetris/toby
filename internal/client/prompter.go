// The client's approval prompter holder. The foreground run registers its modal
// prompter here while it owns the terminal; the daemon's approval.prompt callbacks
// are dispatched to it. When nothing is registered (before the tool starts or after
// it exits), a prompt is denied — the safe default.

package client

import (
	"context"
	"sync"

	sandboxapi "petris.dev/toby/sandbox"
)

// prompter holds the active foreground approval prompter.
type prompter struct {
	mu     sync.Mutex
	active sandboxapi.ApprovalPrompter
}

func newPrompter() *prompter { return &prompter{} }

// Set installs (or clears, with nil) the active prompter. It matches the
// RegisterPrompter callback the foreground exec expects.
func (p *prompter) Set(active sandboxapi.ApprovalPrompter) {
	p.mu.Lock()
	p.active = active
	p.mu.Unlock()
}

// prompt forwards to the active prompter, denying when none is registered.
func (p *prompter) prompt(ctx context.Context, req sandboxapi.ApprovalRequest) (bool, error) {
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	if active == nil {
		return false, nil
	}
	return active.PromptApproval(ctx, req)
}
