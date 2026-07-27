package resourcepool

// Clones host-only HTTP endpoint metadata at process and connector boundaries.

import (
	"net/http"

	"petris.dev/toby/internal/mcpgateway/httpbridge"
)

func cloneUpstream(upstream httpbridge.Upstream) httpbridge.Upstream {
	headers := make(http.Header, len(upstream.Headers))
	for name, values := range upstream.Headers {
		headers[name] = append([]string(nil), values...)
	}

	return httpbridge.Upstream{
		Endpoint:   upstream.Endpoint,
		Headers:    headers,
		HTTPClient: upstream.HTTPClient,
	}
}
