// Package stdio adapts a reader/writer pair — a process's stdin+stdout, or a
// hijacked Docker attach stream — into a net.Conn, plus a one-shot net.Listener
// so a gRPC server can Serve over that single connection. The sandbox manager
// and the host carry their entire gRPC link over the container's stdio this way.
package stdio

import (
	"io"
	"net"
	"sync"
	"time"
)

type addr struct{}

func (addr) Network() string { return "stdio" }
func (addr) String() string  { return "stdio" }

// Conn is a net.Conn backed by an independent reader and writer. Deadlines are
// no-ops: the underlying pipes cannot honor them, and the gRPC link is configured
// without keepalive so it never relies on them.
type Conn struct {
	r io.Reader
	w io.Writer

	closeOnce sync.Once
	closeErr  error
	onClose   func() error
}

// NewConn builds a Conn from r and w. onClose, if non-nil, runs once on Close
// (e.g. to close the underlying attach stream).
func NewConn(r io.Reader, w io.Writer, onClose func() error) *Conn {
	return &Conn{r: r, w: w, onClose: onClose}
}

func (c *Conn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *Conn) Write(p []byte) (int, error) { return c.w.Write(p) }

func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		if c.onClose != nil {
			c.closeErr = c.onClose()
		}
	})
	return c.closeErr
}

func (c *Conn) LocalAddr() net.Addr              { return addr{} }
func (c *Conn) RemoteAddr() net.Addr             { return addr{} }
func (c *Conn) SetDeadline(time.Time) error      { return nil }
func (c *Conn) SetReadDeadline(time.Time) error  { return nil }
func (c *Conn) SetWriteDeadline(time.Time) error { return nil }

// Listener yields a single preset Conn once, then blocks until Close. It lets
// grpc.Server.Serve drive one connection (the stdio link).
type Listener struct {
	mu     sync.Mutex
	conn   net.Conn
	done   chan struct{}
	closed bool
}

// NewListener returns a Listener whose first Accept yields conn.
func NewListener(conn net.Conn) *Listener {
	return &Listener{conn: conn, done: make(chan struct{})}
}

func (l *Listener) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *Listener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		close(l.done)
	}
	return nil
}

func (l *Listener) Addr() net.Addr { return addr{} }
