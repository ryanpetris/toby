package httpbridge

// Tracks only the initialization metadata required by the HTTP transport.

import (
	"encoding/json"
	"errors"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	methodInitialize  = "initialize"
	methodInitialized = "notifications/initialized"
)

type sessionState struct {
	mu sync.Mutex

	initializeID   jsonrpc.ID
	initializeSeen bool
	initialized    bool
	protocol       string
	sessionID      string

	ready     chan struct{}
	readyOnce sync.Once

	messageLimit     chan struct{}
	messageLimitOnce sync.Once
	messageLimitErr  error
}

type sessionIdentity struct {
	protocolVersion string
	sessionID       string
}

func newSessionState() *sessionState {
	return &sessionState{
		ready:        make(chan struct{}),
		messageLimit: make(chan struct{}),
	}
}

func (s *sessionState) observeDownstream(message jsonrpc.Message) {
	request, ok := message.(*jsonrpc.Request)
	if !ok || request.Method != methodInitialize || !request.ID.IsValid() {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.initializeSeen {
		s.initializeSeen = true
		s.initializeID = request.ID
	}
}

func (s *sessionState) observeAcceptedDownstream(message jsonrpc.Message) {
	request, ok := message.(*jsonrpc.Request)
	if !ok || request.Method != methodInitialized || request.ID.IsValid() {
		return
	}

	s.mu.Lock()
	s.initialized = true
	s.signalReadyLocked()
	s.mu.Unlock()
}

func (s *sessionState) observeUpstream(message jsonrpc.Message) error {
	response, ok := message.(*jsonrpc.Response)
	if !ok || response.Error != nil {
		return nil
	}

	s.mu.Lock()
	isInitialize := s.initializeSeen && response.ID == s.initializeID
	s.mu.Unlock()
	if !isInitialize {
		return nil
	}

	var result mcp.InitializeResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return err
	}
	if result.ProtocolVersion == "" {
		return errors.New("initialize response omitted protocolVersion")
	}

	s.mu.Lock()
	s.protocol = result.ProtocolVersion
	s.signalReadyLocked()
	s.mu.Unlock()

	return nil
}

func (s *sessionState) observeSessionID(sessionID string) {
	if sessionID == "" {
		return
	}

	s.mu.Lock()
	s.sessionID = sessionID
	s.signalReadyLocked()
	s.mu.Unlock()
}

func (s *sessionState) protocolVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.protocol
}

func (s *sessionState) hasSession() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.sessionID != ""
}

func (s *sessionState) markMessageLimitExceeded(limit int) {
	s.messageLimitOnce.Do(func() {
		s.mu.Lock()
		s.messageLimitErr = messageLimitError(limit)
		s.mu.Unlock()
		close(s.messageLimit)
	})
}

func (s *sessionState) messageLimitExceeded() <-chan struct{} {
	return s.messageLimit
}

func (s *sessionState) messageLimitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.messageLimitErr == nil {
		return ErrMessageTooLarge
	}
	return s.messageLimitErr
}

func (s *sessionState) hasMessageLimitError() bool {
	select {
	case <-s.messageLimit:
		return true
	default:
		return false
	}
}

func (s *sessionState) initializationComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.initialized
}

func (s *sessionState) waitReady() <-chan struct{} {
	return s.ready
}

func (s *sessionState) identity() sessionIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()

	return sessionIdentity{
		protocolVersion: s.protocol,
		sessionID:       s.sessionID,
	}
}

func (s *sessionState) signalReadyLocked() {
	if !s.initialized || s.protocol == "" {
		return
	}

	s.readyOnce.Do(func() {
		close(s.ready)
	})
}
