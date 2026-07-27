package connector

// Defines the host-side destination selected by a connector handshake.

import (
	"context"
	"io"
)

// Target owns a successfully authenticated connector stream after its
// handshake. The context is canceled when the peer fully disconnects or the
// endpoint is revoked; the caller closes the stream when the target returns.
type Target interface {
	// ServeConnector serves one accepted connector stream.
	ServeConnector(context.Context, io.ReadWriteCloser)
}
