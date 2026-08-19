package client

// Connects to the per-user socket, coordinates agent autostart, and exposes
// status and run-acquisition operations to launch CLIs.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"sync"
	"syscall"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Launcher starts a detached `tobyd` process. Concurrent callers
// in different processes are resolved by socket election.
type Launcher interface {
	// Launch starts a detached agent process.
	Launch(context.Context) error
}

// ServiceOptions configures protocol operations and bounded agent startup.
type ServiceOptions struct {
	Session        Options
	StartupTimeout time.Duration
	RetryMinimum   time.Duration
	RetryMaximum   time.Duration
	Warnings       *warning.Service
	Logger         *diagnostic.Logger
}

func (o ServiceOptions) withDefaults() ServiceOptions {
	o.Session = o.Session.withDefaults()
	if o.StartupTimeout <= 0 {
		o.StartupTimeout = 15 * time.Second
	}
	if o.RetryMinimum <= 0 {
		o.RetryMinimum = 10 * time.Millisecond
	}
	if o.RetryMaximum <= 0 {
		o.RetryMaximum = 250 * time.Millisecond
	}
	if o.RetryMaximum < o.RetryMinimum {
		o.RetryMaximum = o.RetryMinimum
	}

	return o
}

// Service is the reusable launch-side agent entry point.
type Service struct {
	path     string
	version  string
	launcher Launcher
	options  ServiceOptions

	startMu   sync.Mutex
	starting  bool
	started   bool
	startDone chan struct{}
	startErr  error
}

// NewService constructs a client service for one resolved per-user agent
// socket.
func NewService(
	path string,
	binaryVersion string,
	launcher Launcher,
	options ServiceOptions,
) (*Service, error) {
	if path == "" {
		return nil, fmt.Errorf("agent socket path is required")
	}
	if binaryVersion == "" {
		return nil, fmt.Errorf("agent binary version is required")
	}

	return &Service{
		path:     path,
		version:  binaryVersion,
		launcher: launcher,
		options:  options.withDefaults(),
	}, nil
}

// Connect opens a persistent version-1 agent session, starting the service
// when necessary.
func (m *Service) Connect(
	ctx context.Context,
	handler HostActionHandler,
) (*AgentSession, error) {
	return m.openReadySession(ctx, handler, true)
}

// OpenAgent opens a persistent version-1 agent session without starting a
// missing agent.
func (m *Service) OpenAgent(
	ctx context.Context,
	handler HostActionHandler,
) (*AgentSession, error) {
	if m == nil {
		return nil, fmt.Errorf("agent client service is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("agent client context is nil")
	}

	return m.openReadySession(ctx, handler, false)
}

func (m *Service) openReadySession(
	ctx context.Context,
	handler HostActionHandler,
	autostart bool,
) (*AgentSession, error) {
	timeout := m.options.Session.HandshakeTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if autostart {
		timeout = m.options.StartupTimeout
	}
	startupCtx, cancel := boundedContext(ctx, timeout)
	defer cancel()

	delay := m.options.RetryMinimum
	var lastErr error
	for {
		var connection net.Conn
		var err error
		if autostart {
			connection, err = m.ensureConnection(startupCtx)
		} else {
			connection, err = socket.Dial(
				startupCtx,
				m.path,
				socket.Options{Logger: m.options.Logger},
			)
		}
		if err != nil {
			return nil, err
		}

		session, err := m.openAgentConnection(
			startupCtx,
			connection,
			handler,
		)
		if err == nil {
			return session, nil
		}
		if !transientHelloError(err) {
			return nil, err
		}
		lastErr = err

		timer := time.NewTimer(delay)
		select {
		case <-startupCtx.Done():
			timer.Stop()
			return nil, fmt.Errorf(
				"wait for agent readiness: %w: last connection error: %v",
				startupCtx.Err(),
				lastErr,
			)
		case <-timer.C:
		}
		delay *= 2
		if delay > m.options.RetryMaximum {
			delay = m.options.RetryMaximum
		}
	}
}

func (m *Service) openAgentConnection(
	ctx context.Context,
	connection net.Conn,
	handler HostActionHandler,
) (*AgentSession, error) {
	options := m.options.Session
	options.Logger = m.options.Logger

	session, err := OpenAgent(
		ctx,
		connection,
		m.version,
		options,
		m.options.Warnings,
		handler,
	)
	if err != nil {
		return nil, err
	}

	return session, nil
}

func (m *Service) ensureConnection(
	ctx context.Context,
) (net.Conn, error) {
	connection, err := socket.Dial(
		ctx,
		m.path,
		socket.Options{Logger: m.options.Logger},
	)
	if err == nil {
		return connection, nil
	}
	if !agentAbsent(err) {
		return nil, err
	}
	if m.launcher == nil {
		return nil, fmt.Errorf(
			"agent is not running and autostart is unavailable: %w",
			err,
		)
	}
	if err := m.start(ctx); err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}
	defer m.resetStart()

	startupCtx, cancel := boundedContext(ctx, m.options.StartupTimeout)
	defer cancel()

	delay := m.options.RetryMinimum
	lastErr := err
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-startupCtx.Done():
			return nil, fmt.Errorf(
				"wait for agent readiness: %w: last connection error: %v",
				startupCtx.Err(),
				lastErr,
			)
		case <-timer.C:
		}

		connection, dialErr := socket.Dial(
			startupCtx,
			m.path,
			socket.Options{Logger: m.options.Logger},
		)
		if dialErr == nil {
			return connection, nil
		}
		if !agentAbsent(dialErr) &&
			!transientStartupError(dialErr) {
			return nil, dialErr
		}
		lastErr = dialErr

		timer.Reset(delay)
		delay *= 2
		if delay > m.options.RetryMaximum {
			delay = m.options.RetryMaximum
		}
	}
}

// Status reads safe agent state without triggering autostart.
func (m *Service) Status(
	ctx context.Context,
) (protocol.ServiceStatusResponse, error) {
	session, err := m.OpenAgent(ctx, nil)
	if err != nil {
		return protocol.ServiceStatusResponse{}, err
	}
	defer func() {
		m.options.Logger.DebugError(
			"close agent status session",
			session.Close(),
		)
	}()

	return session.Status(ctx)
}

// Stop asks the agent to begin graceful shutdown without starting a missing
// agent. A systemd socket may remain active and start it again later.
func (m *Service) Stop(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("agent client service is nil")
	}
	if ctx == nil {
		return fmt.Errorf("agent client context is nil")
	}

	session, err := m.OpenAgent(ctx, nil)
	if err != nil {
		return err
	}
	if err := session.Stop(ctx); err != nil {
		m.options.Logger.DebugError(
			"close agent stop session",
			session.Close(),
		)
		return err
	}

	m.options.Logger.DebugError(
		"close agent stop session",
		session.Close(),
	)
	return nil
}

func (m *Service) start(ctx context.Context) error {
	m.startMu.Lock()
	if m.starting {
		done := m.startDone
		m.startMu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			m.startMu.Lock()
			defer m.startMu.Unlock()
			return m.startErr
		}
	}
	if m.started {
		err := m.startErr
		m.startMu.Unlock()
		return err
	}
	m.starting = true
	m.startDone = make(chan struct{})
	done := m.startDone
	m.startMu.Unlock()

	// Another caller may have completed agent startup between this caller's
	// first failed dial and winning the in-process launch election.
	session, openErr := m.OpenAgent(ctx, nil)
	if openErr == nil {
		openErr = session.Close()
	}
	var err error
	switch {
	case openErr == nil:
	case !agentAbsent(openErr):
		err = openErr
	default:
		err = m.launcher.Launch(ctx)
	}

	m.startMu.Lock()
	m.startErr = err
	m.started = err == nil
	m.starting = false
	close(done)
	m.startMu.Unlock()

	return err
}

func (m *Service) resetStart() {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.started = false
	m.startErr = nil
}

func agentAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ECONNREFUSED)
}

func transientStartupError(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, io.EOF)
}

func transientHelloError(err error) bool {
	if err == nil {
		return false
	}
	if agentAbsent(err) || transientStartupError(err) {
		return true
	}
	var remote RemoteError
	if errors.As(err, &remote) {
		return remote.Retryable
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if st, ok := status.FromError(current); ok {
			switch st.Code() {
			case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
				return true
			}
		}
	}
	return false
}
