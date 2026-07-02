// Server end: an http.Server upgrades each request at /rpc to a WebSocket and feeds
// the resulting net.Conn to Accept through a channel (the same chanListener pattern
// used by the in-container tunnel server). The upgrade handler blocks for the
// connection's lifetime so the ws library keeps it open until the daemon closes it.

package websocket

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"

	"golang.org/x/net/websocket"
)

// Accept returns the next upgraded connection, starting the server on first call.
func (s *Service) Accept() (net.Conn, error) {
	s.mu.Lock()
	if s.server == nil {
		if err := s.listen(); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	conns, closed := s.conns, s.closed
	s.mu.Unlock()

	select {
	case c := <-conns:
		return c, nil
	case <-closed:
		return nil, net.ErrClosed
	}
}

// listen binds the address and starts serving; the caller holds s.mu.
func (s *Service) listen() error {
	listener, err := net.Listen("tcp", s.address)
	if err != nil {
		return err
	}

	s.conns = make(chan *conn)
	s.closed = make(chan struct{})

	mux := http.NewServeMux()
	mux.Handle(path, websocket.Handler(s.serveConn))
	s.server = &http.Server{Handler: mux}
	go s.server.Serve(listener)
	return nil
}

// serveConn hands one upgraded connection to Accept and blocks until it is closed,
// so the ws library does not tear the connection down early.
func (s *Service) serveConn(ws *websocket.Conn) {
	done := make(chan struct{})
	var once sync.Once
	c := newConn(ws, func() { once.Do(func() { close(done) }) })
	select {
	case s.conns <- c:
		<-done
	case <-s.closed:
	}
}

// Close stops accepting and shuts the server down.
func (s *Service) Close() error {
	s.mu.Lock()
	server := s.server
	closed := s.closed
	s.server = nil
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	if closed != nil {
		close(closed)
	}
	err := server.Shutdown(context.Background())
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
