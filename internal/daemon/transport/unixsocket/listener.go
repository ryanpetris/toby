// Server end: the daemon binds the socket and accepts connections. It deliberately
// does NOT take the client's flock — the client holds that lock across spawn+wait, so
// only one daemon is ever spawned. Should two daemons somehow race, removeStaleSocket
// (which refuses to clobber a live socket) plus net.Listen's own EADDRINUSE make the
// loser fail its bind and exit, leaving exactly one listener.

package unixsocket

import (
	"fmt"
	"net"
)

// Accept returns the next inbound connection, binding the socket on first call.
func (s *Service) Accept() (net.Conn, error) {
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if listener == nil {
		bound, err := s.listen()
		if err != nil {
			return nil, err
		}
		listener = bound
	}
	return listener.Accept()
}

// listen binds the socket, replacing a stale socket left by a crashed daemon.
func (s *Service) listen() (net.Listener, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(s.socket); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", s.socket)
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", s.socket, err)
	}

	s.mu.Lock()
	s.listener = listener
	s.mu.Unlock()
	return listener, nil
}

// Close stops accepting and removes the socket file.
func (s *Service) Close() error {
	s.mu.Lock()
	listener := s.listener
	s.listener = nil
	s.mu.Unlock()
	if listener == nil {
		return nil
	}
	return listener.Close()
}
