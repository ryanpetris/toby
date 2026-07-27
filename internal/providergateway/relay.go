package providergateway

// Relays byte-exact loopback TCP connections to the current Caddy data Unix
// socket while retaining one generation connector for each accepted stream.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"petris.dev/toby/internal/diagnostic"
)

const (
	defaultRelayMaxConnections = 256
	initialAcceptRetryDelay    = 5 * time.Millisecond
	maximumAcceptRetryDelay    = time.Second
)

type relayConnectorOpener func() (Connector, error)
type relayDataDialer func(context.Context) (*net.UnixConn, error)

type relayOptions struct {
	MaxConnections int
	Logger         *diagnostic.Logger
}

func (o relayOptions) normalized() (relayOptions, error) {
	if o.MaxConnections < 0 {
		return relayOptions{}, fmt.Errorf(
			"models gateway connection limit is invalid",
		)
	}
	if o.MaxConnections == 0 {
		o.MaxConnections = defaultRelayMaxConnections
	}

	return o, nil
}

type relayTarget struct {
	generation uint64
	dial       relayDataDialer
	open       relayConnectorOpener
}

type relayConnection struct {
	client    *net.TCPConn
	upstream  *net.UnixConn
	connector Connector

	closeOnce sync.Once
	closeErr  error
	done      chan struct{}
}

type relay struct {
	listener *net.TCPListener
	cancel   context.CancelFunc

	mu          sync.Mutex
	target      relayTarget
	connections map[*relayConnection]struct{}
	permits     chan struct{}
	serveErr    error
	closed      bool

	done chan struct{}

	logger *diagnostic.Logger
}

func newRelay(options relayOptions) (*relay, error) {
	listener, err := net.ListenTCP(
		"tcp4",
		&net.TCPAddr{IP: net.ParseIP("127.0.0.1")},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"listen on models gateway loopback: %w",
			err,
		)
	}

	return newRelayWithListener(listener, options)
}

func newRelayWithListener(
	listener *net.TCPListener,
	options relayOptions,
) (*relay, error) {
	if listener == nil {
		return nil, fmt.Errorf(
			"models gateway loopback listener is required",
		)
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok ||
		address.IP == nil ||
		!address.IP.Equal(net.ParseIP("127.0.0.1")) {
		options.Logger.DebugError(
			"close invalid models gateway listener",
			listener.Close(),
		)
		return nil, fmt.Errorf(
			"models gateway listener must bind only IPv4 loopback",
		)
	}
	normalized, err := options.normalized()
	if err != nil {
		options.Logger.DebugError(
			"close models gateway listener after option validation failure",
			listener.Close(),
		)
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())
	result := &relay{
		listener:    listener,
		cancel:      cancel,
		connections: make(map[*relayConnection]struct{}),
		permits:     make(chan struct{}, normalized.MaxConnections),
		done:        make(chan struct{}),
		logger:      normalized.Logger,
	}
	go result.serve(lifetime)

	return result, nil
}

func (r *relay) baseURL() string {
	if r == nil || r.listener == nil {
		return ""
	}

	address, ok := r.listener.Addr().(*net.TCPAddr)
	if !ok || address.Port <= 0 {
		return ""
	}

	return "http://127.0.0.1:" + strconv.Itoa(address.Port)
}

func (r *relay) publish(
	generation uint64,
	dial relayDataDialer,
	open relayConnectorOpener,
) error {
	if r == nil {
		return fmt.Errorf("models gateway relay is nil")
	}
	if generation == 0 {
		return fmt.Errorf(
			"models gateway target generation is invalid",
		)
	}
	if dial == nil {
		return fmt.Errorf(
			"models gateway data dialer is required",
		)
	}
	if open == nil {
		return fmt.Errorf(
			"models gateway connector opener is required",
		)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return fmt.Errorf("models gateway relay is closed")
	}
	if r.target.generation > generation {
		return fmt.Errorf(
			"models gateway target generation is stale",
		)
	}
	r.target = relayTarget{
		generation: generation,
		dial:       dial,
		open:       open,
	}

	return nil
}

func (r *relay) unpublish(generation uint64) {
	if r == nil || generation == 0 {
		return
	}

	r.mu.Lock()
	if r.target.generation == generation {
		r.target = relayTarget{}
	}
	r.mu.Unlock()
}

func (r *relay) Done() <-chan struct{} {
	if r == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return r.done
}

func (r *relay) Err() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	return r.serveErr
}

func (r *relay) Close() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	if r.closed {
		done := r.done
		r.mu.Unlock()
		<-done
		return nil
	}
	r.closed = true
	r.target = relayTarget{}
	r.cancel()
	listener := r.listener
	connections := make([]*relayConnection, 0, len(r.connections))
	for connection := range r.connections {
		connections = append(connections, connection)
	}
	done := r.done
	r.mu.Unlock()

	result := listener.Close()
	for _, connection := range connections {
		result = errors.Join(result, connection.closeResult())
	}
	<-done

	return result
}

func (r *relay) serve(ctx context.Context) {
	defer close(r.done)

	var retryDelay time.Duration
	for {
		client, err := r.listener.AcceptTCP()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}

			var temporaryError interface {
				Temporary() bool
			}
			if !errors.As(err, &temporaryError) ||
				!temporaryError.Temporary() {
				r.recordServeFailure(err)
				return
			}
			if retryDelay == 0 {
				retryDelay = initialAcceptRetryDelay
			} else {
				retryDelay = min(
					retryDelay*2,
					maximumAcceptRetryDelay,
				)
			}
			timer := time.NewTimer(retryDelay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}
			continue
		}
		retryDelay = 0

		target, ok := r.currentTarget()
		if !ok {
			r.logger.DebugError(
				"close unassigned models gateway connection",
				client.Close(),
			)
			continue
		}
		if !r.acquirePermit() {
			r.logger.DebugError(
				"close excess models gateway connection",
				client.Close(),
			)
			continue
		}
		go r.connect(ctx, client, target)
	}
}

func (r *relay) recordServeFailure(err error) {
	r.mu.Lock()
	if !r.closed {
		r.serveErr = fmt.Errorf(
			"models gateway relay stopped unexpectedly: %w",
			err,
		)
	}
	r.mu.Unlock()
	r.logger.ErrorError(
		"models gateway relay stopped unexpectedly",
		err,
	)
}

func (r *relay) acquirePermit() bool {
	select {
	case r.permits <- struct{}{}:
		return true
	default:
		return false
	}
}

func (r *relay) releasePermit() {
	<-r.permits
}

func (r *relay) currentTarget() (relayTarget, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed ||
		r.target.generation == 0 ||
		r.target.dial == nil ||
		r.target.open == nil {
		return relayTarget{}, false
	}

	return r.target, true
}

func (r *relay) connect(
	ctx context.Context,
	client *net.TCPConn,
	target relayTarget,
) {
	defer r.releasePermit()

	connector, err := target.open()
	if err != nil || connector == nil {
		r.logger.DebugError(
			"open models gateway connector",
			err,
		)
		r.logger.DebugError(
			"close models gateway connection after connector failure",
			client.Close(),
		)
		return
	}

	upstream, err := target.dial(ctx)
	if err != nil {
		connector.Close()
		r.logger.DebugError("dial models gateway upstream", err)
		r.logger.DebugError(
			"close models gateway connection after dial failure",
			client.Close(),
		)
		return
	}

	connection := &relayConnection{
		client:    client,
		upstream:  upstream,
		connector: connector,
		done:      make(chan struct{}),
	}
	if !r.register(connection) {
		r.logger.DebugError(
			"close unpublished models gateway connection",
			connection.closeResult(),
		)
		return
	}
	defer r.unregister(connection)

	go func() {
		select {
		case <-ctx.Done():
			connection.requestClose()
		case <-connector.Done():
			connection.requestClose()
		case <-connection.done:
		}
	}()

	copyErr := connection.copy()
	closeErr := connection.closeResult()
	if err := errors.Join(copyErr, closeErr); err != nil &&
		!errors.Is(err, net.ErrClosed) {
		r.logger.DebugError(
			"relay models gateway connection",
			err,
		)
	}
}

func (r *relay) register(connection *relayConnection) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.closed {
		return false
	}
	r.connections[connection] = struct{}{}

	return true
}

func (r *relay) unregister(connection *relayConnection) {
	r.mu.Lock()
	delete(r.connections, connection)
	r.mu.Unlock()
}

func (c *relayConnection) copy() error {
	results := make(chan error, 2)
	go func() {
		_, copyErr := io.Copy(c.upstream, c.client)
		results <- errors.Join(copyErr, c.upstream.CloseWrite())
	}()
	go func() {
		_, copyErr := io.Copy(c.client, c.upstream)
		results <- errors.Join(copyErr, c.client.CloseWrite())
	}()

	return errors.Join(<-results, <-results)
}

func (c *relayConnection) requestClose() {
	if c == nil {
		return
	}

	c.closeOnce.Do(func() {
		c.closeErr = errors.Join(
			c.client.Close(),
			c.upstream.Close(),
		)
		c.connector.Close()
		close(c.done)
	})
}

func (c *relayConnection) closeResult() error {
	if c == nil {
		return nil
	}

	c.requestClose()
	return c.closeErr
}
