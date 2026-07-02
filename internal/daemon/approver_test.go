package daemon

// The peer-forwarding approval prompter must turn a PromptApproval into an
// approval.prompt call over the client's peer and decode the decision back.

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/protocol"
	sandboxapi "petris.dev/toby/sandbox"
)

func TestPeerPrompterForwards(t *testing.T) {
	daemonConn, clientConn := net.Pipe()
	ctx := context.Background()

	var gotAction, gotSession string
	clientHandler := func(_ context.Context, data []byte) ([]byte, error) {
		req, err := control.DecodeRequest(data)
		if err != nil {
			return nil, err
		}
		var p protocol.ApprovalPromptParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err
		}
		gotAction, gotSession = p.Action, p.SessionID
		return control.ResponseOK(req.ID, protocol.ApprovalPromptResult{Allow: true}), nil
	}

	clientPeer := control.NewPeer(ctx, clientConn, clientHandler)
	clientPeer.Start(nil)
	defer clientPeer.Close()
	daemonPeer := control.NewPeer(ctx, daemonConn, nil)
	daemonPeer.Start(nil)
	defer daemonPeer.Close()

	pp := newPeerPrompter(daemonPeer, "s-7")
	allow, err := pp.PromptApproval(ctx, sandboxapi.ApprovalRequest{Action: "git.push", Name: "Git push", Message: "push main"})
	if err != nil {
		t.Fatalf("PromptApproval: %v", err)
	}
	if !allow {
		t.Fatal("expected allow=true")
	}
	if gotAction != "git.push" {
		t.Fatalf("forwarded action = %q, want git.push", gotAction)
	}
	if gotSession != "s-7" {
		t.Fatalf("forwarded session = %q, want s-7", gotSession)
	}
}
