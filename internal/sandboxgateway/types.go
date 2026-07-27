package sandboxgateway

// Defines resource-opening contracts shared by the endpoint and launch
// resource registry.

import (
	"context"
	"io"
)

// Opener opens one independent byte stream for a registered client resource.
type Opener interface {
	// OpenResource opens an independent stream to the registered resource.
	OpenResource(context.Context) (io.ReadWriteCloser, error)
}

// OpenFunc adapts a function to Opener.
type OpenFunc func(context.Context) (io.ReadWriteCloser, error)

var _ Opener = OpenFunc(nil)

// OpenResource calls f.
func (f OpenFunc) OpenResource(
	ctx context.Context,
) (io.ReadWriteCloser, error) {
	return f(ctx)
}
