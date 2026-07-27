package caddy

// Defines conservative defaults and hard ceilings for administration traffic.

import (
	"time"

	"petris.dev/toby/internal/diagnostic"
)

const (
	defaultRequestTimeout       = 10 * time.Second
	maximumRequestTimeout       = time.Minute
	defaultMaxConfigBodyBytes   = int64(16 << 20)
	maximumConfigBodyBytes      = defaultMaxConfigBodyBytes
	defaultMaxResponseBodyBytes = int64(64 << 10)
	maximumResponseBodyBytes    = int64(1 << 20)
	maxResponseHeaderBytes      = int64(16 << 10)
	maxProbeBodyBytes           = int64(64)
	maxIdleConnections          = 1
	maxConnectionsPerHost       = 4
)

// Options bounds every administration request and response.
type Options struct {
	RequestTimeout       time.Duration
	MaxConfigBodyBytes   int64
	MaxResponseBodyBytes int64
	Logger               *diagnostic.Logger
}

func (o Options) normalized() (Options, error) {
	if o.RequestTimeout < 0 ||
		o.RequestTimeout > maximumRequestTimeout ||
		o.MaxConfigBodyBytes < 0 ||
		o.MaxConfigBodyBytes > maximumConfigBodyBytes ||
		o.MaxResponseBodyBytes < 0 ||
		o.MaxResponseBodyBytes > maximumResponseBodyBytes {
		return Options{}, ErrInvalidOptions
	}

	if o.RequestTimeout == 0 {
		o.RequestTimeout = defaultRequestTimeout
	}
	if o.MaxConfigBodyBytes == 0 {
		o.MaxConfigBodyBytes = defaultMaxConfigBodyBytes
	}
	if o.MaxResponseBodyBytes == 0 {
		o.MaxResponseBodyBytes = defaultMaxResponseBodyBytes
	}

	return o, nil
}
