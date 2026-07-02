// peerPrompter forwards an approval request to the client that owns a session. The
// project's approval service consults it as the active prompter; PromptApproval turns
// into an approval.prompt call over that client's control peer, and the client drives
// its foreground modal to get the user's decision.

package daemon

import (
	"context"
	"encoding/json"

	sandboxapi "petris.dev/toby/sandbox"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/protocol"
)

var _ sandboxapi.ApprovalPrompter = (*peerPrompter)(nil)

type peerPrompter struct {
	peer      *control.Peer
	sessionID string
}

func newPeerPrompter(peer *control.Peer, sessionID string) *peerPrompter {
	return &peerPrompter{peer: peer, sessionID: sessionID}
}

func (p *peerPrompter) PromptApproval(ctx context.Context, req sandboxapi.ApprovalRequest) (bool, error) {
	resp, err := p.peer.Call(ctx, protocol.MethodApprovalPrompt, protocol.ApprovalPromptParams{
		SessionID: p.sessionID,
		Action:    req.Action,
		Name:      req.Name,
		Message:   req.Message,
	})
	if err != nil {
		return false, err
	}
	if resp.Result == nil {
		return false, nil
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return false, err
	}
	var result protocol.ApprovalPromptResult
	if err := json.Unmarshal(data, &result); err != nil {
		return false, err
	}
	return result.Allow, nil
}
