package client

// Declares client time bounds and non-secret remote protocol errors.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/shutdown"
)

// HostActionHandler dispatches agent-originated host actions through the live
// launch CLI. A non-empty response remains authoritative even when err is
// non-nil so JSON-RPC handlers can return a logical error response.
type HostActionHandler interface {
	// Handle processes one reverse host-action request.
	Handle(
		context.Context,
		json.RawMessage,
	) (json.RawMessage, error)
}

// Options bounds handshake, request, and best-effort close operations.
type Options struct {
	HandshakeTimeout   time.Duration
	RequestTimeout     time.Duration
	ReleaseTimeout     time.Duration
	MaxHostActionCalls int
	Logger             *diagnostic.Logger
}

func (o Options) withDefaults() Options {
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = 5 * time.Second
	}
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = 2 * time.Minute
	}
	if o.ReleaseTimeout <= 0 {
		o.ReleaseTimeout = shutdown.ClientResourceReleaseGrace
	}
	if o.MaxHostActionCalls <= 0 {
		o.MaxHostActionCalls = 64
	}

	return o
}

// ServiceStopping describes the remaining cleanup time advertised by an agent
// that is draining this agent session.
type ServiceStopping struct {
	GracePeriod time.Duration
}

// RemoteError is a bounded agent error response. It never contains a resource
// specification, endpoint capability, or credential.
type RemoteError struct {
	CorrelationID protocol.CorrelationID
	Code          protocol.ErrorCode
	Message       string
	Retryable     bool
}

// Error returns the human-readable failure message.
func (e RemoteError) Error() string {
	return fmt.Sprintf("agent %s: %s", e.Code, e.Message)
}
