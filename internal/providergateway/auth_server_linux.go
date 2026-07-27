//go:build linux

package providergateway

// Owns the protected agent-lifetime authorization Unix socket and bounded
// HTTP server that Caddy alone consumes inside its sandbox.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/diagnostic"
)

const (
	defaultAuthReadTimeout    = 2 * time.Second
	defaultAuthWriteTimeout   = 2 * time.Second
	defaultAuthIdleTimeout    = 15 * time.Second
	defaultAuthMaxHeaderBytes = 16 << 10
	defaultAuthMaxConcurrent  = 256
)

type authServerOptions struct {
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	MaxHeaderBytes int
	MaxConcurrent  int
	Logger         *diagnostic.Logger
}

func (o authServerOptions) normalized() (authServerOptions, error) {
	if o.ReadTimeout < 0 ||
		o.WriteTimeout < 0 ||
		o.IdleTimeout < 0 ||
		o.MaxHeaderBytes < 0 ||
		o.MaxConcurrent < 0 {
		return authServerOptions{}, fmt.Errorf(
			"provider authorization server limits must not be negative",
		)
	}
	if o.ReadTimeout == 0 {
		o.ReadTimeout = defaultAuthReadTimeout
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = defaultAuthWriteTimeout
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = defaultAuthIdleTimeout
	}
	if o.MaxHeaderBytes == 0 {
		o.MaxHeaderBytes = defaultAuthMaxHeaderBytes
	}
	if o.MaxConcurrent == 0 {
		o.MaxConcurrent = defaultAuthMaxConcurrent
	}

	return o, nil
}

type authServer struct {
	path     string
	listener *socket.Listener
	server   *http.Server
	logger   *diagnostic.Logger

	mu      sync.Mutex
	closing bool
	err     error

	closeOnce sync.Once
	serveDone chan struct{}
	closeDone chan struct{}
}

func newAuthServer(
	ctx context.Context,
	path string,
	routes *routeStore,
	options authServerOptions,
) (*authServer, error) {
	if ctx == nil {
		return nil, fmt.Errorf(
			"provider authorization server context is nil",
		)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	handler, err := newAuthHandler(routes)
	if err != nil {
		return nil, err
	}

	election, err := socket.Elect(
		ctx,
		path,
		socket.Options{Logger: normalized.Logger},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"open provider authorization socket: %w",
			err,
		)
	}
	if election.Conn != nil {
		normalized.Logger.DebugError(
			"close occupied provider authorization socket connection",
			election.Conn.Close(),
		)
		return nil, fmt.Errorf(
			"provider authorization socket is already in use",
		)
	}
	if election.Listener == nil {
		return nil, fmt.Errorf(
			"provider authorization socket election returned no listener",
		)
	}

	bounded := newConcurrentAuthHandler(
		handler,
		normalized.MaxConcurrent,
	)
	server := &http.Server{
		Handler:           bounded,
		ReadHeaderTimeout: normalized.ReadTimeout,
		ReadTimeout:       normalized.ReadTimeout,
		WriteTimeout:      normalized.WriteTimeout,
		IdleTimeout:       normalized.IdleTimeout,
		MaxHeaderBytes:    normalized.MaxHeaderBytes,
		ErrorLog:          normalized.Logger.StandardLogger(slog.LevelDebug),
	}
	result := &authServer{
		path:      path,
		listener:  election.Listener,
		server:    server,
		logger:    normalized.Logger,
		serveDone: make(chan struct{}),
		closeDone: make(chan struct{}),
	}
	go result.serve()

	return result, nil
}

func (s *authServer) Path() string {
	if s == nil {
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return ""
	}

	return s.path
}

func (s *authServer) File() (*os.File, error) {
	if s == nil || s.listener == nil {
		return nil, net.ErrClosed
	}

	return s.listener.File()
}

func (s *authServer) Generation() (uint64, uint64) {
	if s == nil || s.listener == nil {
		return 0, 0
	}

	return s.listener.Generation()
}

func (s *authServer) Done() <-chan struct{} {
	if s == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return s.serveDone
}

func (s *authServer) Err() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	return s.err
}

func (s *authServer) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf(
			"provider authorization server close context is nil",
		)
	}

	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()

		go s.finishClose(ctx)
	})

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closeDone:
		return s.Err()
	}
}

func (s *authServer) serve() {
	defer close(s.serveDone)

	err := s.server.Serve(s.listener)

	s.mu.Lock()
	closing := s.closing
	if !closing &&
		!errors.Is(err, http.ErrServerClosed) &&
		!errors.Is(err, net.ErrClosed) {
		s.err = fmt.Errorf(
			"provider authorization service stopped unexpectedly",
		)
	}
	s.mu.Unlock()
}

func (s *authServer) finishClose(ctx context.Context) {
	shutdownErr := s.server.Shutdown(ctx)
	if shutdownErr != nil {
		s.logger.DebugError(
			"shut down provider authorization server",
			shutdownErr,
		)
		s.logger.DebugError(
			"close provider authorization server",
			s.server.Close(),
		)
	}
	listenerErr := s.listener.Close()
	s.logger.DebugError(
		"close provider authorization listener",
		listenerErr,
	)
	<-s.serveDone

	close(s.closeDone)
}

type concurrentAuthHandler struct {
	next    http.Handler
	permits chan struct{}
}

var _ http.Handler = (*concurrentAuthHandler)(nil)

func newConcurrentAuthHandler(
	next http.Handler,
	maxConcurrent int,
) *concurrentAuthHandler {
	return &concurrentAuthHandler{
		next:    next,
		permits: make(chan struct{}, maxConcurrent),
	}
}

func (h *concurrentAuthHandler) ServeHTTP(
	writer http.ResponseWriter,
	request *http.Request,
) {
	select {
	case h.permits <- struct{}{}:
		defer func() {
			<-h.permits
		}()
	default:
		writeAuthorizationDenied(writer)
		return
	}

	if request.ContentLength > 0 ||
		len(request.TransferEncoding) != 0 {
		writeAuthorizationDenied(writer)
		return
	}

	h.next.ServeHTTP(writer, request)
}
