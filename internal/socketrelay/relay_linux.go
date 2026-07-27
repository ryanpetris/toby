//go:build linux

package socketrelay

// Relays sandbox streams to host sockets using the launch process's retained
// host credentials and supplementary groups.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/storage/safefs"
)

const relayConnectionLimit = 128

type relayConnection struct {
	client   *net.UnixConn
	upstream *net.UnixConn

	closeOnce sync.Once
	closeErr  error
}

func (c *relayConnection) close() error {
	if c == nil {
		return nil
	}

	c.closeOnce.Do(func() {
		if c.client != nil {
			c.closeErr = errors.Join(c.closeErr, c.client.Close())
		}
		if c.upstream != nil {
			c.closeErr = errors.Join(c.closeErr, c.upstream.Close())
		}
	})

	return normalizeRelayError(c.closeErr)
}

type relay struct {
	hostSocket string
	listener   *privateListener
	context    context.Context
	cancel     context.CancelFunc
	permits    chan struct{}
	acceptDone chan struct{}

	mu          sync.Mutex
	closed      bool
	serveErr    error
	connections map[*relayConnection]struct{}

	handlers sync.WaitGroup
	logger   *diagnostic.Logger
}

func newRelay(
	ctx context.Context,
	root *safefs.Directory,
	logger *diagnostic.Logger,
	name string,
	hostSocket string,
) (*relay, error) {
	probe, err := dialHostSocket(ctx, hostSocket)
	if err != nil {
		return nil, fmt.Errorf(
			"connect host socket %q with host credentials: %w",
			hostSocket,
			err,
		)
	}
	if err := probe.Close(); err != nil {
		logger.DebugError(
			"close host socket access probe",
			err,
			"host_socket",
			hostSocket,
		)
	}

	listener, err := listenPrivate(root, logger, name)
	if err != nil {
		return nil, err
	}

	lifetime, cancel := context.WithCancel(context.Background())
	result := &relay{
		hostSocket: hostSocket,
		listener:   listener,
		context:    lifetime,
		cancel:     cancel,
		permits:    make(chan struct{}, relayConnectionLimit),
		acceptDone: make(chan struct{}),
		connections: make(
			map[*relayConnection]struct{},
		),
		logger: logger,
	}
	go result.accept()

	return result, nil
}

func dialHostSocket(
	ctx context.Context,
	hostSocket string,
) (*net.UnixConn, error) {
	info, err := os.Stat(hostSocket)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("host path is not a Unix socket")
	}

	raw, err := (&net.Dialer{}).DialContext(
		ctx,
		"unix",
		hostSocket,
	)
	if err != nil {
		return nil, err
	}
	connection, ok := raw.(*net.UnixConn)
	if !ok {
		err := fmt.Errorf(
			"host socket returned %T, want *net.UnixConn",
			raw,
		)
		diagnostic.DiscardError(
			"host socket validation already failed",
			"close rejected host socket connection",
			raw.Close(),
		)
		return nil, err
	}

	return connection, nil
}

func (r *relay) File() (*os.File, error) {
	if r == nil || r.listener == nil {
		return nil, net.ErrClosed
	}

	return r.listener.File()
}

func (r *relay) accept() {
	defer close(r.acceptDone)

	for {
		client, err := r.listener.Accept()
		if err != nil {
			if r.context.Err() != nil ||
				errors.Is(err, net.ErrClosed) {
				return
			}

			r.fail(fmt.Errorf("accept socket relay connection: %w", err))
			return
		}

		select {
		case r.permits <- struct{}{}:
		default:
			r.logConnectionError(client.Close())
			continue
		}

		connection := &relayConnection{client: client}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			<-r.permits
			r.logConnectionError(connection.close())
			continue
		}
		r.connections[connection] = struct{}{}
		r.handlers.Add(1)
		r.mu.Unlock()

		go r.handle(connection)
	}
}

func (r *relay) handle(connection *relayConnection) {
	defer r.handlers.Done()
	defer func() {
		r.mu.Lock()
		delete(r.connections, connection)
		r.mu.Unlock()

		r.logConnectionError(connection.close())
		<-r.permits
	}()

	upstream, err := dialHostSocket(r.context, r.hostSocket)
	if err != nil {
		return
	}

	r.mu.Lock()
	if r.closed || r.context.Err() != nil {
		r.mu.Unlock()
		r.logConnectionError(upstream.Close())
		return
	}
	connection.upstream = upstream
	r.mu.Unlock()

	r.logConnectionError(relayStreams(connection.client, upstream))
}

func relayStreams(
	client *net.UnixConn,
	upstream *net.UnixConn,
) error {
	results := make(chan error, 2)
	go copyStream(results, upstream, client)
	go copyStream(results, client, upstream)

	firstErr := normalizeRelayError(<-results)
	var closeErr error
	if firstErr != nil {
		closeErr = errors.Join(client.Close(), upstream.Close())
	}
	secondErr := normalizeRelayError(<-results)

	return errors.Join(
		firstErr,
		secondErr,
		normalizeRelayError(closeErr),
	)
}

func copyStream(
	results chan<- error,
	destination *net.UnixConn,
	source *net.UnixConn,
) {
	_, err := io.Copy(destination, source)
	closeErr := destination.CloseWrite()
	results <- errors.Join(err, closeErr)
}

func (r *relay) fail(err error) {
	err = normalizeRelayError(err)
	r.mu.Lock()
	connections := make(
		[]*relayConnection,
		0,
		len(r.connections),
	)
	for connection := range r.connections {
		connections = append(connections, connection)
	}
	r.mu.Unlock()

	r.cancel()
	for _, connection := range connections {
		err = errors.Join(err, connection.close())
	}
	r.recordError(err)
}

func (r *relay) recordError(err error) {
	err = normalizeRelayError(err)
	if err == nil {
		return
	}

	r.mu.Lock()
	r.serveErr = errors.Join(r.serveErr, err)
	r.mu.Unlock()
}

func (r *relay) logConnectionError(err error) {
	err = normalizeRelayError(err)
	if err == nil {
		return
	}

	r.logger.DebugError(
		"relay socket connection",
		err,
		"host_socket",
		r.hostSocket,
	)
}

func (r *relay) Close() error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	if r.closed {
		done := r.acceptDone
		r.mu.Unlock()
		<-done
		r.handlers.Wait()
		return nil
	}
	r.closed = true
	r.cancel()
	connections := make(
		[]*relayConnection,
		0,
		len(r.connections),
	)
	for connection := range r.connections {
		connections = append(connections, connection)
	}
	r.mu.Unlock()

	result := r.listener.Close()
	for _, connection := range connections {
		result = errors.Join(result, connection.close())
	}
	<-r.acceptDone
	r.handlers.Wait()

	r.mu.Lock()
	serveErr := r.serveErr
	r.mu.Unlock()

	return errors.Join(
		normalizeRelayError(result),
		serveErr,
	)
}

func normalizeRelayError(err error) error {
	switch {
	case err == nil,
		errors.Is(err, io.EOF),
		errors.Is(err, net.ErrClosed),
		errors.Is(err, context.Canceled),
		errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.ECONNRESET):
		return nil
	default:
		return err
	}
}
