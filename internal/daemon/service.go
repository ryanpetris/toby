// Package daemon is the long-lived Toby host process. It owns the transport
// endpoint, accepts client connections, and dispatches their JSON-RPC requests
// through a control.Router. The project and resource registries hang off this
// process as they are built out; for now it serves the daemon.* control methods
// (ping/status/stop).
package daemon

import (
	"bufio"
	"context"
	"errors"
	"net"
	"sync"

	"petris.dev/toby/internal/control"
	"petris.dev/toby/internal/daemon/transport"
)

// Service accepts client connections and serves the control methods registered on
// the router.
type Service struct {
	listener transport.Listener
	router   *control.Router
	fatal    func(error)

	mu     sync.Mutex
	peers  map[*control.Peer]struct{}
	closed bool
}

func newService(listener transport.Listener, router *control.Router, fatal func(error)) *Service {
	return &Service{
		listener: listener,
		router:   router,
		fatal:    fatal,
		peers:    map[*control.Peer]struct{}{},
	}
}

// serve accepts connections until the listener is closed. A non-close Accept error
// means the endpoint is unusable (e.g. the bind failed) — the daemon reports it and
// stops rather than spinning silently.
func (s *Service) serve(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if s.isClosed() || errors.Is(err, net.ErrClosed) {
				return
			}
			if s.fatal != nil {
				s.fatal(err)
			}
			return
		}
		s.addPeer(ctx, conn)
	}
}

func (s *Service) addPeer(ctx context.Context, conn net.Conn) {
	// The handler injects the peer into the request context so session handlers can
	// call back to this specific client (e.g. approval prompts). peer is captured by
	// reference and is set before any request is dispatched.
	var peer *control.Peer
	handler := func(hctx context.Context, data []byte) ([]byte, error) {
		return s.handle(withPeer(hctx, peer), data)
	}
	peer = control.NewPeer(ctx, conn, handler)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		return
	}
	s.peers[peer] = struct{}{}
	s.mu.Unlock()

	peer.Start(bufio.NewReader(conn))
	go func() {
		<-peer.Done()
		s.mu.Lock()
		delete(s.peers, peer)
		s.mu.Unlock()
	}()
}

// handle decodes an inbound request and dispatches it through the router.
func (s *Service) handle(ctx context.Context, data []byte) ([]byte, error) {
	req, err := control.DecodeRequest(data)
	if err != nil {
		return control.ResponseError(nil, control.CodeInvalidRequest, err.Error(), nil), nil
	}
	return s.router.Handle(ctx, req)
}

// close stops accepting and tears down live peers.
func (s *Service) close() error {
	s.mu.Lock()
	s.closed = true
	peers := make([]*control.Peer, 0, len(s.peers))
	for peer := range s.peers {
		peers = append(peers, peer)
	}
	s.peers = map[*control.Peer]struct{}{}
	s.mu.Unlock()

	err := s.listener.Close()
	for _, peer := range peers {
		_ = peer.Close()
	}
	return err
}

func (s *Service) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
