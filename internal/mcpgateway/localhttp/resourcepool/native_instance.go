package resourcepool

// Exposes one ready sidecar generation through an origin-locked HTTP client
// whose transport dials only that generation's private Unix socket.

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"petris.dev/toby/internal/mcpgateway/httpbridge"
	"petris.dev/toby/internal/mcpgateway/sidecar"
)

type nativeInstance struct {
	process   *sidecar.Process
	transport *http.Transport
	upstream  httpbridge.Upstream
}

var (
	_ fmt.Stringer = (*nativeInstance)(nil)
	_ Instance     = (*nativeInstance)(nil)
)

func newNativeInstance(
	process *sidecar.Process,
	hostSocket string,
	requestPath string,
) *nativeInstance {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(
			ctx context.Context,
			_, _ string,
		) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(
				ctx,
				"unix",
				hostSocket,
			)
		},
		DisableCompression: true,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(
			*http.Request,
			[]*http.Request,
		) error {
			return http.ErrUseLastResponse
		},
	}
	instance := &nativeInstance{
		process:   process,
		transport: transport,
		upstream: httpbridge.Upstream{
			Endpoint:   "http://mcp.local" + requestPath,
			HTTPClient: client,
		},
	}
	go func() {
		<-process.Done()
		transport.CloseIdleConnections()
	}()

	return instance
}

func (i *nativeInstance) Upstream() (httpbridge.Upstream, error) {
	if i == nil || i.process == nil {
		return httpbridge.Upstream{}, fmt.Errorf(
			"local HTTP MCP generation is unavailable",
		)
	}
	select {
	case <-i.process.Done():
		return httpbridge.Upstream{}, fmt.Errorf(
			"local HTTP MCP generation is unavailable",
		)
	default:
		return cloneUpstream(i.upstream), nil
	}
}

func (i *nativeInstance) Done() <-chan struct{} {
	if i == nil || i.process == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return i.process.Done()
}

func (i *nativeInstance) Err() error {
	if i == nil || i.process == nil {
		return nil
	}

	return i.process.Err()
}

func (i *nativeInstance) Stop(ctx context.Context) error {
	if i == nil || i.process == nil {
		return nil
	}

	return i.process.Stop(ctx)
}

func (i *nativeInstance) Kill(ctx context.Context) error {
	if i == nil || i.process == nil {
		return nil
	}

	return i.process.Kill(ctx)
}

func (*nativeInstance) String() string {
	return "{Process:<redacted> Upstream:<redacted>}"
}
