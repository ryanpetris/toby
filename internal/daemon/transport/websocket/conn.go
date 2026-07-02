// Package websocket is the WebSocket transport for the client<->daemon channel. It
// exists alongside the unix-socket transport to validate the transport seam: it
// carries the exact same newline-framed JSON-RPC payloads control.Peer writes, over
// a ws message stream instead of a socket. The future goal is a carrier that also
// works on Windows.
package websocket

import (
	"net"
	"time"

	"golang.org/x/net/websocket"
)

// conn adapts a WebSocket connection to a net.Conn so control.Peer's byte-stream
// framing rides unchanged. Each net.Conn Write becomes one binary ws message; Read
// serves bytes out of the most recently received message, buffering any remainder so
// a partial read never drops data. Both ends use the Message codec, so framing stays
// consistent regardless of message boundaries.
type conn struct {
	ws      *websocket.Conn
	buf     []byte
	onClose func()
}

var _ net.Conn = (*conn)(nil)

func newConn(ws *websocket.Conn, onClose func()) *conn {
	return &conn{ws: ws, onClose: onClose}
}

func (c *conn) Read(p []byte) (int, error) {
	if len(c.buf) == 0 {
		var msg []byte
		if err := websocket.Message.Receive(c.ws, &msg); err != nil {
			return 0, err
		}
		c.buf = msg
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *conn) Write(p []byte) (int, error) {
	if err := websocket.Message.Send(c.ws, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *conn) Close() error {
	err := c.ws.Close()
	if c.onClose != nil {
		c.onClose()
	}
	return err
}

func (c *conn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *conn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *conn) SetDeadline(t time.Time) error      { return c.ws.SetDeadline(t) }
func (c *conn) SetReadDeadline(t time.Time) error  { return c.ws.SetReadDeadline(t) }
func (c *conn) SetWriteDeadline(t time.Time) error { return c.ws.SetWriteDeadline(t) }
