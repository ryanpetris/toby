// Client end: dial the daemon's /rpc endpoint, and — when nothing is listening —
// spawn a detached daemon and poll until the port accepts. The OS single-binds the
// TCP port, so "bind wins" is the race guard; losers just re-dial.

package websocket

import (
	"context"
	"fmt"
	"net"
	"time"

	"petris.dev/toby/internal/daemon/transport"

	"golang.org/x/net/websocket"
)

// Connect dials the running daemon and wraps the ws connection as a net.Conn.
func (s *Service) Connect(ctx context.Context) (net.Conn, error) {
	ws, err := websocket.Dial(s.Endpoint(), "", "http://"+s.address)
	if err != nil {
		return nil, err
	}
	return newConn(ws, nil), nil
}

// EnsureDaemon makes a daemon reachable, spawning one if the port is not accepting.
func (s *Service) EnsureDaemon(ctx context.Context) error {
	if s.probe() {
		return nil
	}
	if err := transport.SpawnDaemon(); err != nil {
		return err
	}
	return s.waitReady(ctx)
}

// probe reports whether something is accepting on the address right now.
func (s *Service) probe() bool {
	conn, err := net.DialTimeout("tcp", s.address, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (s *Service) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if s.probe() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("websocket: daemon did not come up at %s", s.address)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
