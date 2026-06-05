package control

// WebSocket transport: thin adapters over coder/websocket that yield a net.Conn,
// so the Peer (newline-delimited JSON) is unaware of framing. acceptWebSocket
// upgrades a server request; DialEndpoint dials the control channel as a client.

import (
	"context"
	"net"
	"net/http"

	"github.com/coder/websocket"
)

// maxControlMessage bounds a single control message. JSON-RPC params can carry
// file contents (CreateParams.Data), so the default 32 KiB limit is far too
// small.
const maxControlMessage = 64 << 20 // 64 MiB

// WebSocketHandler adapts a ConnHandler into an http.Handler that upgrades the
// request to a control WebSocket and serves the connection for its lifetime.
// Mount it as an ordinary Route; the server's token middleware gates it when the
// route sets Auth.
func WebSocketHandler(handler ConnHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := acceptWebSocket(r.Context(), w, r)
		if err != nil {
			return
		}
		handler(r.Context(), conn)
	})
}

// acceptWebSocket upgrades an incoming control request to a WebSocket and returns
// it as a net.Conn bound to ctx (closing ctx closes the conn). Auth is handled by
// the server's token middleware before this runs.
func acceptWebSocket(ctx context.Context, w http.ResponseWriter, r *http.Request) (net.Conn, error) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxControlMessage)
	return websocket.NetConn(ctx, c, websocket.MessageBinary), nil
}

// DialEndpoint dials the control WebSocket and returns it as a net.Conn bound to
// ctx. The token, when set, is sent as a bearer Authorization header.
func DialEndpoint(ctx context.Context, endpoint Endpoint) (net.Conn, error) {
	opts := &websocket.DialOptions{}
	if endpoint.Token != "" {
		opts.HTTPHeader = http.Header{"Authorization": []string{"Bearer " + endpoint.Token}}
	}

	c, _, err := websocket.Dial(ctx, endpoint.ControlURL(), opts)
	if err != nil {
		return nil, err
	}
	c.SetReadLimit(maxControlMessage)
	return websocket.NetConn(ctx, c, websocket.MessageBinary), nil
}
