package providergateway

// Observes HTTP connection closure without closing the caller-owned stream.

import (
	"net"
	"sync"
)

type connectionWithoutClose struct {
	net.Conn
}

var _ net.Conn = connectionWithoutClose{}

func (connectionWithoutClose) Close() error {
	return nil
}

type observedConnection struct {
	net.Conn
	close func()
	once  sync.Once
}

var _ net.Conn = (*observedConnection)(nil)

func (c *observedConnection) Close() error {
	c.once.Do(c.close)
	return nil
}
