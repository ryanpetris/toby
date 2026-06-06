package tunnel

import "net/url"

// ProxyAddr is where the in-sandbox manager binds its local HTTP proxy listener.
// It lives in the container's own network namespace, so it is always loopback; we
// use a dedicated address in 127.0.0.0/8 (the whole range routes to lo) to avoid
// colliding with anything a tool binds on 127.0.0.1. Host and sandbox share this
// constant in the same binary, so nothing needs to be passed between them.
const ProxyAddr = "127.77.0.1:47600"

// ProxyBaseURL builds the in-sandbox proxied base URL for a registered target id.
// The path scheme matches the host reverse proxy's parser (/proxy/<id>).
func ProxyBaseURL(id string) string {
	return "http://" + ProxyAddr + "/proxy/" + url.PathEscape(id)
}
