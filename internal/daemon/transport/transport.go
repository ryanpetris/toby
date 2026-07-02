// Package transport is the swappable seam for the Toby client<->daemon channel. It
// defines the interfaces both ends depend on and nothing else: a concrete transport
// (unix socket, WebSocket, …) only has to yield net.Conns and own its endpoint's
// bind/spawn/cleanup. Because control.Peer already carries all JSON-RPC framing over
// a net.Conn, every transport carries the exact same payloads; only the byte carrier
// differs.
package transport

import (
	"context"
	"net"
)

// Listener is the server end: the daemon accepts connections and wraps each in a
// control.Peer. It is Accept-based (rather than exposing net.Listener) so a
// message-oriented carrier such as WebSocket can satisfy it by feeding upgraded
// connections through an internal channel.
type Listener interface {
	// Accept returns the next inbound connection, blocking until one arrives or the
	// listener is closed.
	Accept() (net.Conn, error)
	// Endpoint is a human-readable address for logs and status (never assumed to be
	// a filesystem path).
	Endpoint() string
	// Close stops accepting and releases the endpoint.
	Close() error
}

// Connector is the client end: dial an already-running daemon.
type Connector interface {
	Connect(ctx context.Context) (net.Conn, error)
	Endpoint() string
}

// Bootstrap is the client end's detect-or-spawn step. EnsureDaemon makes a daemon
// reachable — for the unix transport it does the flock + stale-socket + detached
// spawn + poll dance; another transport may health-check a remote service instead.
type Bootstrap interface {
	EnsureDaemon(ctx context.Context) error
}
