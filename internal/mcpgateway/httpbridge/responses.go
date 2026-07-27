package httpbridge

// Bounds upstream response bodies until their owning SDK readers close them.

import (
	"errors"
	"io"
	"sync"
)

// Headroom covers every outstanding call, every concurrently dispatched
// non-call, and the persistent standalone event stream.
const maxActiveResponseBodies = maxOutstandingCalls + maxConcurrentWrites + 1

type responseLimiter struct {
	slots chan struct{}
}

type limitedResponseBody struct {
	io.ReadCloser

	closeOnce sync.Once
	closeErr  error
	release   func()
}

var _ io.ReadCloser = (*limitedResponseBody)(nil)

func newResponseLimiter() *responseLimiter {
	return &responseLimiter{
		slots: make(chan struct{}, maxActiveResponseBodies),
	}
}

func (l *responseLimiter) wrap(
	body io.ReadCloser,
) (io.ReadCloser, error) {
	select {
	case l.slots <- struct{}{}:
		return &limitedResponseBody{
			ReadCloser: body,
			release: func() {
				<-l.slots
			},
		}, nil
	default:
		return nil, errors.New(
			"MCP HTTP session exceeded the active response body limit",
		)
	}
}

func (b *limitedResponseBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		b.release()
	})

	return b.closeErr
}
