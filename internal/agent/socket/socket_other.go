//go:build !linux

package socket

// Reports unsupported-platform errors for private Unix sockets.

import (
	"context"
	"fmt"
	"net"
)

// Listener is unavailable outside Linux.
type Listener struct{}

var _ net.Listener = (*Listener)(nil)

// Generation returns no filesystem identity outside Linux.
func (l *Listener) Generation() (uint64, uint64) {
	return 0, 0
}

// Elect reports that private Unix sockets require Linux.
func Elect(context.Context, string, Options) (*Election, error) {
	return nil, fmt.Errorf("%w: socket election requires Linux", ErrUnsupported)
}

// Listen reports that private sockets require Linux.
func Listen(string, Options) (*Listener, error) {
	return nil, fmt.Errorf("%w: socket listening requires Linux", ErrUnsupported)
}

// SystemdListener reports that systemd socket activation requires Linux.
func SystemdListener(string, Options) (*Listener, bool, error) {
	return nil, false, fmt.Errorf(
		"%w: systemd socket activation requires Linux",
		ErrUnsupported,
	)
}

// Dial reports that private Unix sockets require Linux.
func Dial(context.Context, string, Options) (*net.UnixConn, error) {
	return nil, fmt.Errorf("%w: socket dialing requires Linux", ErrUnsupported)
}

// Accept reports that private Unix sockets require Linux.
func (l *Listener) Accept() (net.Conn, error) {
	return nil, fmt.Errorf("%w: socket accepting requires Linux", ErrUnsupported)
}

// Close reports that private Unix sockets require Linux.
func (l *Listener) Close() error {
	return fmt.Errorf("%w: socket closing requires Linux", ErrUnsupported)
}

// Addr returns no address because private Unix sockets require Linux.
func (l *Listener) Addr() net.Addr {
	return nil
}
