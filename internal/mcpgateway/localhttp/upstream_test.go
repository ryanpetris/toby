package localhttp

// Verifies connector metadata cloning preserves host-only dialing policy.

import (
	"net/http"
	"testing"

	"petris.dev/toby/internal/mcpgateway/httpbridge"
)

func TestCloneUpstreamPreservesHTTPClient(t *testing.T) {
	client := &http.Client{}
	source := httpbridge.Upstream{
		Endpoint:   "http://mcp.local/mcp",
		Headers:    http.Header{"X-Test": {"source"}},
		HTTPClient: client,
	}

	cloned := cloneUpstream(source)
	if cloned.HTTPClient != client {
		t.Fatal("clone did not preserve the generation-bound HTTP client")
	}
	cloned.Headers.Set("X-Test", "changed")
	if got := source.Headers.Get("X-Test"); got != "source" {
		t.Fatalf("clone mutated source header to %q", got)
	}
}
