// The control peer that issued a request is threaded through the handler context so
// session handlers can reach it — session.start uses it to install a prompter that
// forwards approval requests back to that specific client.

package daemon

import (
	"context"

	"petris.dev/toby/internal/control"
)

type peerKey struct{}

// withPeer returns ctx carrying the peer that issued the current request.
func withPeer(ctx context.Context, peer *control.Peer) context.Context {
	return context.WithValue(ctx, peerKey{}, peer)
}

// peerFrom returns the issuing peer, or nil if none is set.
func peerFrom(ctx context.Context) *control.Peer {
	peer, _ := ctx.Value(peerKey{}).(*control.Peer)
	return peer
}
