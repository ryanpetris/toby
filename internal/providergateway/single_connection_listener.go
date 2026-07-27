package providergateway

// Presents one supplied models connection as a one-use HTTP listener.

import (
	"net"
	"sync"
)

type singleConnectionListener struct {
	connection net.Conn
	accepted   bool
	done       chan struct{}
	closeOnce  sync.Once
	mu         sync.Mutex
}

var _ net.Listener = (*singleConnectionListener)(nil)

func newSingleConnectionListener(
	connection net.Conn,
) *singleConnectionListener {
	return &singleConnectionListener{
		connection: connection,
		done:       make(chan struct{}),
	}
}

func (l *singleConnectionListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	if !l.accepted {
		l.accepted = true
		connection := &observedConnection{
			Conn:  connectionWithoutClose{Conn: l.connection},
			close: l.signal,
		}
		l.mu.Unlock()
		return connection, nil
	}
	done := l.done
	l.mu.Unlock()

	<-done
	return nil, net.ErrClosed
}

func (l *singleConnectionListener) Close() error {
	l.signal()
	return nil
}

func (l *singleConnectionListener) signal() {
	l.closeOnce.Do(func() {
		close(l.done)
	})
}

func (l *singleConnectionListener) Addr() net.Addr {
	return l.connection.LocalAddr()
}
