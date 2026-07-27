package client

// Owns one gRPC agent session and its reverse host-action stream.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
	"petris.dev/toby/internal/shutdown"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// AgentSession is one opened agent connection.
type AgentSession struct {
	connection *grpc.ClientConn
	client     agentv1.AgentServiceClient
	stream     agentv1.AgentService_OpenSessionClient
	sessionID  protocol.SessionID
	options    Options
	handler    HostActionHandler
	logger     *diagnostic.Logger

	sendMu sync.Mutex
	mu     sync.Mutex

	active        map[protocol.CorrelationID]context.CancelFunc
	closed        bool
	closeErr      error
	stoppingID    protocol.CorrelationID
	stoppingAcked bool

	hostActionContext  context.Context
	cancelHostActions  context.CancelFunc
	hostActionPermits  chan struct{}
	hostActionHandlers sync.WaitGroup

	cancelSession context.CancelFunc
	done          chan struct{}
	readerDone    chan struct{}
	stopping      chan ServiceStopping
	closeOnce     sync.Once
}

// OpenAgent requests agent information, selects a supported protocol, and
// opens a persistent agent session.
func OpenAgent(
	ctx context.Context,
	connection net.Conn,
	binaryVersion string,
	options Options,
	warnings *warning.Service,
	handler HostActionHandler,
) (result *AgentSession, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("open agent session: context is nil")
	}
	if connection == nil {
		return nil, fmt.Errorf(
			"open agent session: connection is nil",
		)
	}
	if binaryVersion == "" {
		return nil, fmt.Errorf(
			"open agent session: binary version is required",
		)
	}

	options = options.withDefaults()
	dialer := &initialDialer{connection: connection}
	grpcConnection, err := grpc.NewClient(
		"passthrough:///toby-agent",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(dialer.DialContext),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(protocol.MaxMessageBytes),
			grpc.MaxCallSendMsgSize(protocol.MaxMessageBytes),
		),
	)
	if err != nil {
		options.Logger.DebugError(
			"close agent dialer after gRPC client setup failed",
			dialer.Close(),
		)
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			options.Logger.DebugError(
				"close agent dialer after agent setup failed",
				dialer.Close(),
			)
			options.Logger.DebugError(
				"close agent gRPC connection after agent setup failed",
				grpcConnection.Close(),
			)
		}
	}()

	client := agentv1.NewAgentServiceClient(grpcConnection)
	handshakeCtx, cancelHandshake := boundedContext(
		ctx,
		options.HandshakeTimeout,
	)
	defer cancelHandshake()

	helloID, err := protocol.NewCorrelationID()
	if err != nil {
		return nil, err
	}
	hello, err := client.Hello(
		handshakeCtx,
		&agentv1.HelloRequest{
			CorrelationId: string(helloID),
			BinaryVersion: binaryVersion,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"request agent hello: %w",
			remoteRequestError(err, helloID),
		)
	}
	if hello.GetCorrelationId() != string(helloID) {
		return nil, fmt.Errorf(
			"agent hello correlation ID %q does not match %q",
			hello.GetCorrelationId(),
			helloID,
		)
	}
	if err := protocol.ValidateBinaryVersion(
		hello.GetBinaryVersion(),
	); err != nil {
		return nil, fmt.Errorf("agent returned an invalid binary version")
	}
	if !protocol.SupportsVersion(hello.GetProtocolVersion()) {
		return nil, protocol.VersionError{
			Received:  hello.GetProtocolVersion(),
			Supported: protocol.SupportedVersions(),
		}
	}
	if hello.GetHashAlgorithm() == "" {
		return nil, fmt.Errorf("agent hello omitted the hash algorithm")
	}
	if hello.GetBinaryVersion() != binaryVersion {
		agentVersion := hello.GetBinaryVersion()
		protocolVersion := hello.GetProtocolVersion()
		warnings.Warn(
			warning.AgentBinaryMismatch,
			fmt.Sprintf(
				"client binary version %q differs from agent binary version %q; continuing because the client supports agent protocol version %d.",
				binaryVersion,
				agentVersion,
				protocolVersion,
			),
			"client_binary_version", binaryVersion,
			"agent_binary_version", agentVersion,
			"protocol_version", protocolVersion,
		)
	}

	sessionCtx, cancelSession := context.WithCancel(context.Background())
	sessionStream, err := client.OpenSession(sessionCtx)
	if err != nil {
		cancelSession()
		return nil, fmt.Errorf("open agent session stream: %w", err)
	}

	openID, err := protocol.NewCorrelationID()
	if err != nil {
		cancelSession()
		return nil, err
	}
	if err := sessionStream.Send(&agentv1.SessionClientMessage{
		CorrelationId: string(openID),
		Value: &agentv1.SessionClientMessage_Open{
			Open: &agentv1.SessionOpenRequest{},
		},
	}); err != nil {
		cancelSession()
		return nil, fmt.Errorf("send agent session open: %w", err)
	}
	openedMessage, err := sessionStream.Recv()
	if err != nil {
		cancelSession()
		return nil, fmt.Errorf(
			"receive agent session open: %w",
			remoteRequestError(err, openID),
		)
	}
	if openedMessage.GetCorrelationId() != string(openID) {
		cancelSession()
		return nil, fmt.Errorf(
			"agent session-open correlation ID %q does not match %q",
			openedMessage.GetCorrelationId(),
			openID,
		)
	}
	opened := openedMessage.GetOpened()
	if opened == nil {
		cancelSession()
		return nil, fmt.Errorf("agent did not return a session-open response")
	}
	sessionID := protocol.SessionID(opened.GetSessionId())
	if err := protocol.ValidateSessionID(sessionID); err != nil {
		cancelSession()
		return nil, fmt.Errorf("agent returned an invalid session ID")
	}

	seenTransports := make(map[protocol.TransportCapability]struct{})
	for _, value := range opened.GetTransportCapabilities() {
		capability, err := protocol.TransportCapabilityFromAgent(value)
		if err != nil {
			cancelSession()
			return nil, err
		}
		if _, duplicate := seenTransports[capability]; duplicate {
			cancelSession()
			return nil, fmt.Errorf(
				"agent session duplicated transport capability %q",
				capability,
			)
		}
		seenTransports[capability] = struct{}{}
	}
	if len(seenTransports) == 0 {
		cancelSession()
		return nil, fmt.Errorf(
			"agent session omitted transport capabilities",
		)
	}

	hostActionContext, cancelHostActions := context.WithCancel(
		context.Background(),
	)
	session := &AgentSession{
		connection:        grpcConnection,
		client:            client,
		stream:            sessionStream,
		sessionID:         sessionID,
		options:           options,
		handler:           handler,
		logger:            options.Logger,
		active:            make(map[protocol.CorrelationID]context.CancelFunc),
		hostActionContext: hostActionContext,
		cancelHostActions: cancelHostActions,
		hostActionPermits: make(chan struct{}, options.MaxHostActionCalls),
		cancelSession:     cancelSession,
		done:              make(chan struct{}),
		readerDone:        make(chan struct{}),
		stopping:          make(chan ServiceStopping, 1),
	}
	go session.read()

	return session, nil
}

// Done closes when the agent session can no longer serve requests.
func (s *AgentSession) Done() <-chan struct{} {
	if s == nil {
		done := make(chan struct{})
		close(done)
		return done
	}

	return s.done
}

// Stopping receives the agent's one bounded shutdown notice. The agent keeps
// the agent stream available for cleanup until the notice is acknowledged or
// its private deadline expires.
func (s *AgentSession) Stopping() <-chan ServiceStopping {
	if s == nil {
		stopping := make(chan ServiceStopping)
		close(stopping)
		return stopping
	}

	return s.stopping
}

// Acquire registers one resource configuration and returns its opaque agent
// identity and independently releasable lease.
func (s *AgentSession) Acquire(
	ctx context.Context,
	kind protocol.ResourceKind,
	configuration json.RawMessage,
) (*ResourceLease, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return nil, err
	}
	if err := kind.Validate(); err != nil {
		return nil, err
	}
	if err := protocol.ValidateConfigurationDocument(configuration); err != nil {
		return nil, err
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return nil, err
	}
	requestCtx, cancel := boundedContext(ctx, s.options.RequestTimeout)
	defer cancel()

	response, err := s.client.AcquireResource(
		requestCtx,
		&agentv1.ResourceAcquireRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
			Kind:          protocol.ResourceKindToAgent(kind),
			Configuration: append([]byte(nil), configuration...),
		},
	)
	if err != nil {
		return nil, remoteRequestError(err, id)
	}
	if err := requireCorrelation(response.GetCorrelationId(), id); err != nil {
		return nil, err
	}

	resourceID := protocol.ResourceID(response.GetResourceId())
	if err := protocol.ValidateResourceID(resourceID); err != nil {
		return nil, fmt.Errorf("agent returned an invalid resource ID")
	}
	leaseID := protocol.LeaseID(response.GetLeaseId())
	if err := protocol.ValidateLeaseID(leaseID); err != nil {
		return nil, fmt.Errorf("agent returned an invalid lease ID")
	}

	return &ResourceLease{
		session:    s,
		resourceID: resourceID,
		leaseID:    leaseID,
	}, nil
}

// Status returns safe agent process state and activity counts.
func (s *AgentSession) Status(
	ctx context.Context,
) (protocol.ServiceStatusResponse, error) {
	if err := s.validateRequestContext(ctx); err != nil {
		return protocol.ServiceStatusResponse{}, err
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return protocol.ServiceStatusResponse{}, err
	}
	requestCtx, cancel := boundedContext(ctx, s.options.RequestTimeout)
	defer cancel()

	response, err := s.client.Status(
		requestCtx,
		&agentv1.StatusRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
		},
	)
	if err != nil {
		return protocol.ServiceStatusResponse{},
			remoteRequestError(err, id)
	}
	if err := requireCorrelation(response.GetCorrelationId(), id); err != nil {
		return protocol.ServiceStatusResponse{}, err
	}
	state, err := protocol.ServiceStateFromAgent(response.GetState())
	if err != nil {
		return protocol.ServiceStatusResponse{}, err
	}

	return protocol.ServiceStatusResponse{
		CorrelationID:   id,
		BinaryVersion:   response.GetBinaryVersion(),
		State:           state,
		ActiveSessions:  response.GetActiveSessions(),
		ActiveLeases:    response.GetActiveLeases(),
		ActiveResources: response.GetActiveResources(),
		ActiveStreams:   response.GetActiveStreams(),
	}, nil
}

// Stop requests graceful agent shutdown.
func (s *AgentSession) Stop(ctx context.Context) error {
	if err := s.validateRequestContext(ctx); err != nil {
		return err
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return err
	}
	requestCtx, cancel := boundedContext(ctx, s.options.RequestTimeout)
	defer cancel()

	response, err := s.client.Stop(
		requestCtx,
		&agentv1.StopRequest{
			CorrelationId: string(id),
			SessionId:     string(s.sessionID),
		},
	)
	if err != nil {
		return remoteRequestError(err, id)
	}

	return requireCorrelation(response.GetCorrelationId(), id)
}

func (s *AgentSession) read() {
	defer close(s.readerDone)
	defer s.markClosed()

	for {
		message, err := s.stream.Recv()
		if err != nil {
			if !errors.Is(err, io.EOF) &&
				!errors.Is(err, net.ErrClosed) &&
				status.Code(err) != codes.Canceled {
				s.setCloseError(remoteError(err))
			}
			return
		}

		switch {
		case message.GetHostActionRequest() != nil:
			s.dispatchHostAction(message)
		case message.GetHostActionCancel() != nil:
			s.cancelHostAction(
				protocol.CorrelationID(message.GetCorrelationId()),
			)
		case message.GetShutdownRequest() != nil:
			s.receiveServiceStopping(message)
		default:
			s.setCloseError(fmt.Errorf(
				"unexpected agent session message",
			))
			s.abort(nil)
			return
		}
	}
}

func (s *AgentSession) receiveServiceStopping(
	message *agentv1.SessionServerMessage,
) {
	id := protocol.CorrelationID(message.GetCorrelationId())
	if err := protocol.ValidateCorrelationID(id); err != nil {
		s.setCloseError(fmt.Errorf(
			"agent sent an invalid shutdown correlation ID",
		))
		s.abort(nil)
		return
	}
	milliseconds := message.GetShutdownRequest().
		GetGracePeriodMilliseconds()
	const maxDurationMilliseconds = uint64(
		(^uint64(0) >> 1) / uint64(time.Millisecond),
	)
	if milliseconds == 0 || milliseconds > maxDurationMilliseconds {
		s.setCloseError(fmt.Errorf(
			"agent sent an invalid shutdown grace period",
		))
		s.abort(nil)
		return
	}

	s.mu.Lock()
	if s.stoppingID != "" {
		s.mu.Unlock()
		s.setCloseError(fmt.Errorf(
			"agent sent more than one shutdown request",
		))
		s.abort(nil)
		return
	}
	s.stoppingID = id
	s.mu.Unlock()

	s.stopping <- ServiceStopping{
		GracePeriod: time.Duration(milliseconds) * time.Millisecond,
	}
}

func (s *AgentSession) send(
	message *agentv1.SessionClientMessage,
) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	return s.stream.Send(message)
}

func (s *AgentSession) setCloseError(err error) {
	s.mu.Lock()
	s.closeErr = errors.Join(s.closeErr, err)
	s.mu.Unlock()
}

func (s *AgentSession) abort(err error) {
	if err != nil {
		s.setCloseError(err)
	}
	s.cancelSession()
	if closeErr := s.connection.Close(); closeErr != nil {
		s.logger.DebugError(
			"close aborted agent connection",
			closeErr,
		)
	}
}

func (s *AgentSession) markClosed() {
	s.closeOnce.Do(func() {
		s.cancelHostActions()

		s.mu.Lock()
		s.closed = true
		for _, cancel := range s.active {
			cancel()
		}
		s.mu.Unlock()

		close(s.done)
	})
}

// Close revokes host actions and closes the session stream. The agent releases
// every session-owned lease as soon as it receives stream EOF.
func (s *AgentSession) Close() error {
	if s == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		shutdown.ClientShutdownGrace,
	)
	defer cancel()
	return s.CloseContext(ctx)
}

// CloseContext revokes host actions and closes the session within the caller's
// cleanup deadline.
func (s *AgentSession) CloseContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	closeSendErr := s.stream.CloseSend()
	s.cancelSession()
	connectionErr := s.connection.Close()

	select {
	case <-s.readerDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	handlersDone := make(chan struct{})
	go func() {
		s.hostActionHandlers.Wait()
		close(handlersDone)
	}()
	select {
	case <-handlersDone:
	case <-ctx.Done():
		return ctx.Err()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.logger.DebugError(
		"close agent request stream",
		closeSendErr,
	)
	s.logger.DebugError(
		"close agent connection",
		connectionErr,
	)
	s.logger.DebugError(
		"finish agent session",
		s.closeErr,
	)
	return nil
}

// AcknowledgeStopping confirms that launch-owned cleanup has completed for the
// agent's active shutdown request.
func (s *AgentSession) AcknowledgeStopping(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	id := s.stoppingID
	if id == "" || s.stoppingAcked {
		s.mu.Unlock()
		return nil
	}
	s.stoppingAcked = true
	s.mu.Unlock()

	result := make(chan error, 1)
	go func() {
		result <- s.send(&agentv1.SessionClientMessage{
			CorrelationId: string(id),
			Value: &agentv1.SessionClientMessage_ShutdownResponse{
				ShutdownResponse: &agentv1.ShutdownResponse{},
			},
		})
	}()

	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		s.abort(ctx.Err())
		return ctx.Err()
	}
}

func requireCorrelation(
	actual string,
	expected protocol.CorrelationID,
) error {
	if actual != string(expected) {
		return fmt.Errorf(
			"agent response correlation ID %q does not match %q",
			actual,
			expected,
		)
	}

	return nil
}
