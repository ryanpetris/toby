package httpbridge

// Defines the caller-supplied HTTP transport and request policy.

import (
	"net/http"
	"time"

	"petris.dev/toby/internal/diagnostic"
)

const (
	defaultCloseTimeout   = 5 * time.Second
	defaultMaxMessageSize = 1 << 20
)

// Options configures a Bridge. HTTPClient may be shared with other users; each
// session preserves its connection pool without modifying the client.
type Options struct {
	// HTTPClient supplies the shared connection pool and client policy.
	HTTPClient *http.Client
	// CloseTimeout bounds the HTTP DELETE that ends a session.
	CloseTimeout time.Duration
	// MaxMessageBytes bounds one downstream JSON line, upstream JSON body, or
	// upstream SSE event. Zero uses one MiB, which is also the maximum accepted
	// value because it matches the pinned SDK's Streamable HTTP ceiling.
	MaxMessageBytes int
	// Logger receives non-fatal transport cleanup diagnostics.
	Logger *diagnostic.Logger
}

// Upstream identifies one MCP Streamable HTTP endpoint. Headers are applied
// only to requests sent to Endpoint's origin.
type Upstream struct {
	// Endpoint is the absolute Streamable HTTP endpoint URL.
	Endpoint string
	// Headers contains host-only request headers such as authorization.
	Headers http.Header
	// HTTPClient overrides the Bridge client for this Serve call. The bridge
	// preserves its transport and policy while isolating mutable session state.
	HTTPClient *http.Client
}

type upstreamSettings struct {
	headers http.Header
}

func validateUpstream(upstream Upstream) (upstreamSettings, origin, error) {
	endpointOrigin, err := parseEndpoint(upstream.Endpoint)
	if err != nil {
		return upstreamSettings{}, origin{}, err
	}

	headers, err := cloneHeaders(upstream.Headers)
	if err != nil {
		return upstreamSettings{}, origin{}, err
	}

	return upstreamSettings{
		headers: headers,
	}, endpointOrigin, nil
}
