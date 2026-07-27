package httpbridge

// Owns one downstream stream and one fresh upstream Streamable HTTP session.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/diagnostic"
)

// Bridge carries independent MCP sessions over one shared HTTP client.
type Bridge struct {
	client          *http.Client
	closeTimeout    time.Duration
	maxMessageBytes int
	logger          *diagnostic.Logger
}

// New constructs a reusable Streamable HTTP bridge.
func New(options Options) (*Bridge, error) {
	closeTimeout := options.CloseTimeout
	if closeTimeout == 0 {
		closeTimeout = defaultCloseTimeout
	}
	if closeTimeout < 0 {
		return nil, errors.New("MCP HTTP close timeout must not be negative")
	}

	maxMessageBytes := options.MaxMessageBytes
	if maxMessageBytes == 0 {
		maxMessageBytes = defaultMaxMessageSize
	}
	if maxMessageBytes < 0 {
		return nil, errors.New("MCP message byte limit must not be negative")
	}
	if maxMessageBytes > defaultMaxMessageSize {
		return nil, fmt.Errorf(
			"MCP message byte limit must not exceed %d bytes",
			defaultMaxMessageSize,
		)
	}

	client := options.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	return &Bridge{
		client:          client,
		closeTimeout:    closeTimeout,
		maxMessageBytes: maxMessageBytes,
		logger:          options.Logger,
	}, nil
}

type writeCloser struct {
	io.Writer
}

var _ io.WriteCloser = writeCloser{}

func (writeCloser) Close() error {
	return nil
}

// Serve bridges downstream newline-delimited MCP traffic to one fresh
// Streamable HTTP session. It returns when either peer disconnects, the caller
// cancels ctx, or either transport fails. On downstream EOF it gives writes
// already admitted to HTTP a short grace period to receive response headers;
// it does not wait for outstanding call results before ending the session.
func (b *Bridge) Serve(
	ctx context.Context,
	downstream io.ReadWriteCloser,
	upstream Upstream,
) error {
	if b == nil || b.client == nil {
		return errors.New("MCP HTTP bridge is required")
	}
	if downstream == nil {
		return errors.New("downstream MCP stream is required")
	}

	settings, endpointOrigin, err := validateUpstream(upstream)
	if err != nil {
		return err
	}

	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()

	state := newSessionState()
	calls := newCallTracker()
	responses := newResponseLimiter()
	baseClient := b.client
	if upstream.HTTPClient != nil {
		baseClient = upstream.HTTPClient
	}
	client, err := sessionHTTPClient(
		baseClient,
		endpointOrigin,
		state,
		responses,
		settings.headers,
		b.closeTimeout,
		b.maxMessageBytes,
		b.logger,
	)
	if err != nil {
		return err
	}

	downstreamConnection, err := (&mcp.IOTransport{
		Reader: newLineLimitReadCloser(downstream, b.maxMessageBytes),
		Writer: writeCloser{Writer: downstream},
	}).Connect(lifetime)
	if err != nil {
		return fmt.Errorf("connect downstream MCP stream: %w", err)
	}

	upstreamConnection, err := (&mcp.StreamableClientTransport{
		Endpoint:   upstream.Endpoint,
		HTTPClient: client,
	}).Connect(lifetime)
	if err != nil {
		b.logger.DebugError(
			"close downstream MCP stream after upstream connection failure",
			downstreamConnection.Close(),
		)
		return fmt.Errorf(
			"connect upstream MCP HTTP transport: %w",
			err,
		)
	}

	results := make(chan relayResult, 3)
	var workers sync.WaitGroup
	workers.Add(3)

	go func() {
		defer workers.Done()
		results <- relayResult{
			side: "downstream",
			err: relayDownstream(
				lifetime,
				downstreamConnection,
				upstreamConnection,
				state,
				calls,
			),
		}
	}()
	go func() {
		defer workers.Done()
		results <- relayResult{
			side: "upstream",
			err: relayUpstream(
				lifetime,
				upstreamConnection,
				downstreamConnection,
				state,
				calls,
			),
		}
	}()
	go func() {
		defer workers.Done()
		if err := receiveStandalone(
			lifetime,
			client,
			upstream.Endpoint,
			state,
			downstreamConnection,
			calls,
			b.maxMessageBytes,
			b.logger,
		); err != nil {
			results <- relayResult{side: "standalone upstream", err: err}
		}
	}()

	var result relayResult
	select {
	case <-ctx.Done():
		result = relayResult{side: "bridge", err: ctx.Err()}
	case <-state.messageLimitExceeded():
		result = relayResult{
			side: "upstream",
			err:  state.messageLimitError(),
		}
	case result = <-results:
	}

	cancel()
	downstreamCloseErr := downstreamConnection.Close()
	workers.Wait()
	upstreamCloseErr := upstreamConnection.Close()
	b.logger.DebugError(
		"close downstream MCP stream",
		downstreamCloseErr,
	)
	b.logger.DebugError(
		"close upstream MCP HTTP session",
		upstreamCloseErr,
	)

	return relayError(result)
}
