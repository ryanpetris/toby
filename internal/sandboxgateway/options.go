package sandboxgateway

// Defines bounded endpoint and message limits.

import (
	"fmt"

	"petris.dev/toby/internal/diagnostic"
)

const (
	maxMessageBytes       = 1 << 20
	maxStreamDataBytes    = 32 << 10
	maxIdentifierBytes    = 256
	defaultMaxConnections = 128
	maximumConnections    = 256
)

// Options configures one run-scoped sandbox endpoint.
type Options struct {
	MaxConnections int
	Logger         *diagnostic.Logger
}

func (o Options) normalized() (Options, error) {
	if o.MaxConnections < 0 ||
		o.MaxConnections > maximumConnections {
		return Options{}, fmt.Errorf(
			"sandbox gateway connection limit must be between 0 and %d",
			maximumConnections,
		)
	}
	if o.MaxConnections == 0 {
		o.MaxConnections = defaultMaxConnections
	}

	return o, nil
}
