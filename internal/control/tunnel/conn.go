package tunnel

import (
	"io"
	"net"
	"sync"
	"time"
)

// chunkStream is the subset shared by the generated client and server bidi
// streams (grpc.BidiStreamingClient/Server[Chunk, Chunk]).
type chunkStream interface {
	Send(*Chunk) error
	Recv() (*Chunk, error)
}

// streamConn adapts a Connect bidi stream into a net.Conn. Written bytes become
// Chunk.Data; read bytes are drained from received Chunks. A Chunk with Close set
// is an EOF marker for that direction (half-close). Deadlines are no-ops — the
// link runs without keepalive. Used on both the host and sandbox ends.
//
// Concurrency: at most one Read (Recv) and one Write (Send) run at once, which gRPC
// streams permit; writeMu serializes Write against CloseWrite/Close.
type streamConn struct {
	stream chunkStream

	readMu  sync.Mutex
	rest    []byte
	readEOF bool

	writeMu     sync.Mutex
	writeClosed bool

	closeOnce sync.Once
	done      chan struct{}
	onClose   func()
}

func newStreamConn(stream chunkStream, onClose func()) *streamConn {
	return &streamConn{stream: stream, done: make(chan struct{}), onClose: onClose}
}

// NewStreamConn adapts a Chunk stream into a net.Conn for higher-level protocols
// carried over the tunnel, such as the JSON-RPC control peer.
func NewStreamConn(stream chunkStream, onClose func()) net.Conn {
	return newStreamConn(stream, onClose)
}

func (c *streamConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for len(c.rest) == 0 {
		if c.readEOF {
			return 0, io.EOF
		}
		chunk, err := c.stream.Recv()
		if err != nil {
			if err == io.EOF {
				c.readEOF = true
				return 0, io.EOF
			}
			return 0, err
		}
		if len(chunk.GetData()) > 0 {
			c.rest = chunk.GetData()
		}
		if chunk.GetClose() {
			c.readEOF = true
		}
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

func (c *streamConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeClosed {
		return 0, net.ErrClosed
	}
	if err := c.stream.Send(&Chunk{Data: p}); err != nil {
		return 0, err
	}
	return len(p), nil
}

// CloseWrite half-closes the write direction: it sends an EOF Chunk so the peer's
// Read returns io.EOF, and (on the client) calls CloseSend.
func (c *streamConn) CloseWrite() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.writeClosed {
		return nil
	}
	c.writeClosed = true
	_ = c.stream.Send(&Chunk{Close: true})
	if cs, ok := c.stream.(interface{ CloseSend() error }); ok {
		return cs.CloseSend()
	}
	return nil
}

func (c *streamConn) Close() error {
	_ = c.CloseWrite()
	c.closeOnce.Do(func() {
		close(c.done)
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

// Done is closed when the conn is Closed; the server's Connect handler waits on it
// so the stream outlives the http.Server's use of the conn.
func (c *streamConn) Done() <-chan struct{} { return c.done }

func (c *streamConn) LocalAddr() net.Addr              { return streamAddr{} }
func (c *streamConn) RemoteAddr() net.Addr             { return streamAddr{} }
func (c *streamConn) SetDeadline(time.Time) error      { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

type streamAddr struct{}

func (streamAddr) Network() string { return "tunnel" }
func (streamAddr) String() string  { return "tunnel" }
