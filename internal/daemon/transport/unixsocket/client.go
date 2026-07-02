// Client end: dial a running daemon, and — when none is reachable — perform the
// flock-guarded detect-or-spawn dance so exactly one detached `toby daemon` is
// started even under concurrent first invocations.

package unixsocket

import (
	"context"
	"fmt"
	"net"
	"time"

	"petris.dev/toby/internal/daemon/transport"
)

// Connect dials the running daemon.
func (s *Service) Connect(ctx context.Context) (net.Conn, error) {
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", s.socket)
}

// EnsureDaemon makes a daemon reachable, spawning one if necessary. Concurrent
// callers serialize on the flock: the winner spawns and holds the lock until the
// daemon is accepting, so the losers block, then re-probe and find the freshly-bound
// socket rather than each spawning their own. Holding the lock through the wait is
// safe because the daemon binds without taking this lock.
func (s *Service) EnsureDaemon(ctx context.Context) error {
	if probe(s.socket) {
		return nil
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	return s.withLock(func() error {
		if probe(s.socket) {
			return nil
		}
		if err := removeStaleSocket(s.socket); err != nil {
			return err
		}
		if err := transport.SpawnDaemon(); err != nil {
			return err
		}
		return s.waitReady(ctx)
	})
}

// waitReady polls the socket until the spawned daemon accepts, bounded by ~5s.
func (s *Service) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if probe(s.socket) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("unixsocket: daemon did not come up at %s", s.socket)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}
