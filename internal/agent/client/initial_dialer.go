package client

// Hands the already-connected agent socket to gRPC exactly once.

import (
	"context"
	"io"
	"net"
	"sync"
)

type initialDialer struct {
	mu         sync.Mutex
	connection net.Conn
}

func (d *initialDialer) DialContext(
	_ context.Context,
	_ string,
) (net.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.connection == nil {
		return nil, io.ErrClosedPipe
	}
	connection := d.connection
	d.connection = nil

	return connection, nil
}

func (d *initialDialer) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.connection == nil {
		return nil
	}
	err := d.connection.Close()
	d.connection = nil

	return err
}
