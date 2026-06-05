// Package control is the authenticated JSON-RPC 2.0 control channel between the
// host and a sandbox, plus its HTTP side-channel.
//
// The transport is a WebSocket (coder/websocket) carried over a chi HTTP server:
// ListenEndpoint mounts a set of routes (the /control WebSocket upgrade via
// WebSocketHandler, the HTTP proxy, binary delivery), and DialEndpoint connects
// from the sandbox side. Both ends expose the connection as a net.Conn, over which
// Peer exchanges newline-framed JSON-RPC messages. This package owns the envelope
// (request/response/error types, error codes, build/parse helpers) and the method
// dispatch Router; the method-specific contract lives in control/methods/*.
package control
