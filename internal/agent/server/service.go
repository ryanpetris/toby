package server

// Serves the typed agent API over a private listener.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
	"petris.dev/toby/internal/resourcehash"

	"google.golang.org/grpc"
)

// Service serves one per-user agent listener.
type Service struct {
	agentv1.UnimplementedAgentServiceServer

	version             string
	resourceCoordinator ResourceCoordinator
	options             Options

	mu              sync.Mutex
	state           protocol.ServiceState
	server          *grpc.Server
	cancelIdle      context.CancelFunc
	serving         bool
	sessions        map[protocol.SessionID]*agentSession
	streams         uint64
	observedSession bool
	activityVersion uint64

	shutdownOnce sync.Once
}

var _ agentv1.AgentServiceServer = (*Service)(nil)

// New constructs an agent. Serve may be called once.
func New(
	binaryVersion string,
	resourceCoordinator ResourceCoordinator,
	options Options,
) (*Service, error) {
	if binaryVersion == "" {
		return nil, fmt.Errorf("agent binary version is required")
	}
	if resourceCoordinator == nil {
		return nil, fmt.Errorf("agent resource coordinator is required")
	}

	options = options.withDefaults()

	return &Service{
		version:             binaryVersion,
		resourceCoordinator: resourceCoordinator,
		options:             options,
		state:               protocol.ServiceStarting,
		sessions:            make(map[protocol.SessionID]*agentSession),
	}, nil
}

// Serve runs the agent API until the context is canceled, Close is called,
// or the listener fails.
func (s *Service) Serve(
	ctx context.Context,
	listener net.Listener,
	options ServeOptions,
) error {
	if s == nil {
		return fmt.Errorf("agent server is nil")
	}
	if ctx == nil {
		return fmt.Errorf("agent server context is nil")
	}
	if listener == nil {
		return fmt.Errorf("agent listener is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(protocol.MaxMessageBytes),
		grpc.MaxSendMsgSize(protocol.MaxMessageBytes),
		grpc.MaxConcurrentStreams(uint32(s.options.MaxConcurrentRPCs)),
	)
	agentv1.RegisterAgentServiceServer(grpcServer, s)

	idleCtx, cancelIdle := context.WithCancel(context.Background())
	if err := s.beginServe(grpcServer, cancelIdle); err != nil {
		cancelIdle()
		return err
	}
	defer s.finishServe()

	stopForContext := context.AfterFunc(ctx, func() {
		s.gracefulStop()
	})
	if !options.Persistent {
		go s.stopWhenIdle(idleCtx)
	}

	err := grpcServer.Serve(listener)
	stopForContext()
	if errors.Is(err, grpc.ErrServerStopped) ||
		errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}

// Close immediately stops the server and cancels every active session.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}

	s.forceStop()
	return nil
}

func (s *Service) beginServe(
	grpcServer *grpc.Server,
	cancelIdle context.CancelFunc,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.serving {
		return fmt.Errorf("agent server may only be served once")
	}
	s.serving = true
	s.server = grpcServer
	s.cancelIdle = cancelIdle
	s.state = protocol.ServiceReady

	return nil
}

func (s *Service) finishServe() {
	s.mu.Lock()
	s.state = protocol.ServiceStopping
	cancelIdle := s.cancelIdle
	s.cancelIdle = nil
	s.server = nil
	s.mu.Unlock()

	if cancelIdle != nil {
		cancelIdle()
	}
}

func (s *Service) forceStop() {
	server, sessions := s.beginShutdown()
	for _, session := range sessions {
		session.cancel()
	}
	if server != nil {
		server.Stop()
	}
}

func (s *Service) gracefulStop() {
	s.shutdownOnce.Do(func() {
		s.runGracefulStop()
	})
}

func (s *Service) runGracefulStop() {
	server, sessions := s.beginShutdown()
	if server == nil {
		return
	}

	clientCtx, cancelClients := context.WithTimeout(
		context.Background(),
		s.options.ClientShutdownGrace,
	)
	advertisedGrace := s.options.ClientShutdownGrace -
		s.options.ClientShutdownMargin

	var clients sync.WaitGroup
	for _, session := range sessions {
		clients.Add(1)
		go func(current *agentSession) {
			defer clients.Done()
			acknowledged, err := current.requestStopping(advertisedGrace)
			if err != nil {
				s.options.Logger.DebugError(
					"request agent-session shutdown",
					err,
					"session_id",
					current.id,
				)
				return
			}
			select {
			case <-acknowledged:
			case <-clientCtx.Done():
			}
		}(session)
	}
	clientsDone := make(chan struct{})
	go func() {
		clients.Wait()
		close(clientsDone)
	}()
	select {
	case <-clientsDone:
	case <-clientCtx.Done():
	}
	cancelClients()

	for _, session := range sessions {
		session.cancel()
	}
	transportDone := make(chan struct{})
	go func() {
		server.GracefulStop()
		close(transportDone)
	}()
	timer := time.NewTimer(s.options.TransportShutdownGrace)
	defer timer.Stop()
	select {
	case <-transportDone:
	case <-timer.C:
		server.Stop()
	}
}

func (s *Service) beginShutdown() (
	*grpc.Server,
	[]*agentSession,
) {
	s.mu.Lock()
	if s.state == protocol.ServiceStopping {
		server := s.server
		s.mu.Unlock()
		return server, nil
	}
	s.state = protocol.ServiceStopping
	sessions := make([]*agentSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	server := s.server
	s.mu.Unlock()

	return server, sessions
}

func (s *Service) serviceState() protocol.ServiceState {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.state
}

func (s *Service) registerSession(session *agentSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state != protocol.ServiceReady {
		return fmt.Errorf("agent is not accepting sessions")
	}
	if _, exists := s.sessions[session.id]; exists {
		return fmt.Errorf("agent session ID collision")
	}
	s.sessions[session.id] = session
	s.observedSession = true
	s.activityVersion++

	return nil
}

func (s *Service) unregisterSession(session *agentSession) {
	s.mu.Lock()
	if s.sessions[session.id] == session {
		delete(s.sessions, session.id)
		s.activityVersion++
	}
	s.mu.Unlock()
}

func (s *Service) session(id protocol.SessionID) *agentSession {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sessions[id]
}

func (s *Service) sessionCount() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return uint64(len(s.sessions))
}

func (s *Service) beginStream() {
	s.mu.Lock()
	s.streams++
	s.activityVersion++
	s.mu.Unlock()
}

func (s *Service) finishStream() {
	s.mu.Lock()
	if s.streams > 0 {
		s.streams--
		s.activityVersion++
	}
	s.mu.Unlock()
}

func (s *Service) streamCount() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.streams
}

func (s *Service) stopWhenIdle(ctx context.Context) {
	startup := time.NewTimer(s.options.StartupGrace)
	defer startup.Stop()

	checks := time.NewTicker(s.options.IdleCheckInterval)
	defer checks.Stop()

	startupExpired := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-startup.C:
			startupExpired = true
		case <-checks.C:
		}

		if s.stopIfIdle(startupExpired) {
			return
		}
	}
}

func (s *Service) stopIfIdle(startupExpired bool) bool {
	s.mu.Lock()
	if s.state != protocol.ServiceReady ||
		(!s.observedSession && !startupExpired) ||
		len(s.sessions) != 0 ||
		s.streams != 0 {
		s.mu.Unlock()
		return false
	}
	activityVersion := s.activityVersion
	s.mu.Unlock()

	resources := s.resourceCoordinator.ResourceSnapshot()

	s.mu.Lock()
	if s.state != protocol.ServiceReady ||
		s.activityVersion != activityVersion ||
		len(s.sessions) != 0 ||
		s.streams != 0 ||
		resources.ActiveLeases != 0 ||
		resources.ActiveResources != 0 {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	go s.gracefulStop()
	return true
}

// Hello reports the agent API version before the client starts a session.
func (s *Service) Hello(
	_ context.Context,
	request *agentv1.HelloRequest,
) (*agentv1.HelloResponse, error) {
	if request == nil {
		return nil, invalidRequest("", "hello request is required")
	}
	correlationID := protocol.CorrelationID(request.GetCorrelationId())
	if err := protocol.ValidateCorrelationID(correlationID); err != nil {
		return nil, invalidRequest(
			request.GetCorrelationId(),
			"hello correlation ID is invalid",
		)
	}
	return &agentv1.HelloResponse{
		CorrelationId:   request.GetCorrelationId(),
		BinaryVersion:   s.version,
		ProtocolVersion: protocol.Version,
		HashAlgorithm:   resourcehash.Algorithm,
	}, nil
}
