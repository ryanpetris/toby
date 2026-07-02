// Socket probing and stale-socket cleanup shared by the client and server ends.

package unixsocket

import (
	"errors"
	"net"
	"os"
	"time"
)

// errDaemonRunning reports that the socket is already served by a live daemon.
var errDaemonRunning = errors.New("unixsocket: daemon already running")

// probe reports whether something is accepting on the socket right now.
func probe(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// removeStaleSocket deletes a socket file left behind by a crashed daemon. If a live
// daemon is accepting on it, it refuses and returns errDaemonRunning so a bind never
// clobbers a running daemon.
func removeStaleSocket(socket string) error {
	if _, err := os.Lstat(socket); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if probe(socket) {
		return errDaemonRunning
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
