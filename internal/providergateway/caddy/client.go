package caddy

// Constructs an HTTP client whose only connection path is a caller-supplied
// Unix-socket capability.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"petris.dev/toby/internal/diagnostic"
)

// Connector opens one stream connection to the exact protected Caddy
// administration Unix socket. Implementations must honor the supplied context.
type Connector func(context.Context) (net.Conn, error)

// Client performs the bounded subset of Caddy administration operations used
// by the models gateway.
type Client struct {
	httpClient       *http.Client
	transport        *http.Transport
	requestTimeout   time.Duration
	maxConfigBytes   int64
	maxResponseBytes int64
	logger           *diagnostic.Logger
	closed           atomic.Bool
}

var (
	_ fmt.Stringer = (*Client)(nil)
	_ io.Closer    = (*Client)(nil)
)

// New constructs a client that ignores all request addresses and proxy
// settings and obtains every connection from connector.
func New(connector Connector, options Options) (*Client, error) {
	if connector == nil {
		return nil, ErrInvalidOptions
	}

	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(
			ctx context.Context,
			_, _ string,
		) (net.Conn, error) {
			connection, err := connector(ctx)
			if err != nil {
				return nil, err
			}
			if connection == nil ||
				connection.RemoteAddr() == nil ||
				connection.RemoteAddr().Network() != "unix" {
				if connection != nil {
					normalized.Logger.DebugError(
						"close rejected Caddy administration connection",
						connection.Close(),
					)
					return nil, errNonUnixConnection
				}
				return nil, errNonUnixConnection
			}

			return connection, nil
		},
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           maxIdleConnections,
		MaxIdleConnsPerHost:    maxIdleConnections,
		MaxConnsPerHost:        maxConnectionsPerHost,
		IdleConnTimeout:        normalized.RequestTimeout,
		ResponseHeaderTimeout:  normalized.RequestTimeout,
		MaxResponseHeaderBytes: maxResponseHeaderBytes,
	}
	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(
			*http.Request,
			[]*http.Request,
		) error {
			return http.ErrUseLastResponse
		},
	}

	return &Client{
		httpClient:       httpClient,
		transport:        transport,
		requestTimeout:   normalized.RequestTimeout,
		maxConfigBytes:   normalized.MaxConfigBodyBytes,
		maxResponseBytes: normalized.MaxResponseBodyBytes,
		logger:           normalized.Logger,
	}, nil
}

// Close permanently prevents new operations and closes idle administration
// connections.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}

	c.closed.Store(true)
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}

	return nil
}

// String withholds client internals and the injected connector.
func (*Client) String() string {
	return "{CaddyAdmin:<redacted>}"
}
