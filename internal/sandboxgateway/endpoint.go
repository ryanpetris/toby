package sandboxgateway

// Owns one private, run-scoped gRPC listener and its resource service.

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/diagnostic"
	sandboxv1 "petris.dev/toby/internal/gen/toby/sandbox/v1"
	"petris.dev/toby/internal/version"

	"google.golang.org/grpc"
)

// Endpoint serves one immutable resource registry at one Unix socket.
type Endpoint struct {
	path       string
	device     uint64
	inode      uint64
	listener   *socket.Listener
	grpcServer *grpc.Server
	done       chan struct{}
	logger     *diagnostic.Logger

	mu       sync.Mutex
	serveErr error

	closeOnce sync.Once
	closeErr  error
}

// Listen creates a private endpoint and starts its gRPC server.
func Listen(
	path string,
	openers map[string]Opener,
	options Options,
) (*Endpoint, error) {
	allowlist, normalized, err := validateResourceService(
		openers,
		options,
	)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("%w: %q", ErrPathInUse, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf(
			"inspect sandbox socket path %q: %w",
			path,
			err,
		)
	}

	listener, err := socket.Listen(
		path,
		socket.Options{Logger: normalized.Logger},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"listen on sandbox socket %q: %w",
			path,
			err,
		)
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMessageBytes),
		grpc.MaxSendMsgSize(maxMessageBytes),
		grpc.MaxConcurrentStreams(
			uint32(normalized.MaxConnections),
		),
	)
	sandboxv1.RegisterSandboxServiceServer(
		grpcServer,
		newService(
			allowlist,
			normalized.MaxConnections,
			version.String(),
			normalized.Logger,
		),
	)
	device, inode := listener.Generation()
	endpoint := &Endpoint{
		path:       path,
		device:     device,
		inode:      inode,
		listener:   listener,
		grpcServer: grpcServer,
		done:       make(chan struct{}),
		logger:     normalized.Logger,
	}
	go endpoint.serve()

	return endpoint, nil
}

// Path returns the diagnostic host path while the endpoint is open.
func (e *Endpoint) Path() string {
	if e == nil {
		return ""
	}

	return e.path
}

// SocketGeneration returns the exact device and inode created for the endpoint.
func (e *Endpoint) SocketGeneration() (uint64, uint64) {
	if e == nil {
		return 0, 0
	}

	return e.device, e.inode
}

// Close revokes the endpoint and every active resource stream.
func (e *Endpoint) Close() error {
	if e == nil {
		return nil
	}

	e.closeOnce.Do(func() {
		e.grpcServer.Stop()
		listenerErr := e.listener.Close()
		<-e.done

		e.mu.Lock()
		serveErr := e.serveErr
		e.mu.Unlock()

		e.logger.DebugError(
			"close sandbox gateway listener",
			normalizeEndpointClose(listenerErr),
			"path", e.path,
		)
		e.closeErr = serveErr
		e.path = ""
	})

	return e.closeErr
}

func (e *Endpoint) serve() {
	defer close(e.done)

	err := e.grpcServer.Serve(e.listener)
	if err == nil ||
		errors.Is(err, grpc.ErrServerStopped) ||
		errors.Is(err, net.ErrClosed) {
		return
	}

	e.mu.Lock()
	e.serveErr = fmt.Errorf(
		"sandbox gRPC service stopped unexpectedly: %w",
		err,
	)
	e.mu.Unlock()
}

func normalizeEndpointClose(err error) error {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}
