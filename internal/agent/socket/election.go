package socket

// Describes the result of agent socket election.

import "net"

// Election contains exactly one non-nil endpoint. Listener is set for the
// process that won agent election. Conn is a connection to the already-running
// agent for every other process.
type Election struct {
	Listener *Listener
	Conn     *net.UnixConn
}
