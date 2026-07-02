// Service is the WebSocket transport. One value satisfies transport.Listener
// (server), transport.Connector and transport.Bootstrap (client); which methods run
// depends on which composition root resolved it. The listen address comes from
// config; TOBY_WS_ADDRESS overrides it for tests.

package websocket

import (
	"net/http"
	"os"
	"sync"
)

const (
	// DefaultAddress is the loopback address the daemon binds when none is configured.
	DefaultAddress = "127.0.0.1:47700"
	// path is the single upgrade endpoint the daemon serves.
	path = "/rpc"
)

// Config is the transport's tunable: the loopback address both ends use.
type Config struct {
	Address string
}

type Service struct {
	address string

	mu     sync.Mutex
	server *http.Server
	conns  chan *conn
	closed chan struct{}
}

func newService(cfg Config) *Service {
	address := cfg.Address
	if override := os.Getenv("TOBY_WS_ADDRESS"); override != "" {
		address = override
	}
	if address == "" {
		address = DefaultAddress
	}
	return &Service{address: address}
}

// Endpoint reports the ws URL for logs and status.
func (s *Service) Endpoint() string { return "ws://" + s.address + path }
