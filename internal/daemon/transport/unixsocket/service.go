// Service is the unix-socket transport. One value satisfies transport.Listener
// (server), transport.Connector and transport.Bootstrap (client); which methods run
// depends on which composition root resolved it.

package unixsocket

import (
	"net"
	"os"
	"path/filepath"
	"sync"

	"petris.dev/toby/config"
)

const (
	dirName    = "toby"
	socketName = "daemon.sock"
	lockName   = "daemon.lock"
)

type Service struct {
	dir    string
	socket string
	lock   string

	mu       sync.Mutex
	listener net.Listener
}

func newService(paths config.Paths) *Service {
	dir := filepath.Join(paths.RuntimeDir, dirName)
	return &Service{
		dir:    dir,
		socket: filepath.Join(dir, socketName),
		lock:   filepath.Join(dir, lockName),
	}
}

// Endpoint reports the socket path for logs and status.
func (s *Service) Endpoint() string { return s.socket }

// ensureDir creates the runtime subdirectory 0700 so the socket and lock are never
// world-accessible.
func (s *Service) ensureDir() error {
	return os.MkdirAll(s.dir, 0o700)
}
