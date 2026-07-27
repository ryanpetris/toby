package client

// Provides shared request deadlines and typed gRPC error decoding.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"

	"google.golang.org/grpc/status"
)

func boundedContext(
	parent context.Context,
	limit time.Duration,
) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}

	deadline := time.Now().Add(limit)
	if parentDeadline, ok := parent.Deadline(); ok &&
		parentDeadline.Before(deadline) {
		return context.WithCancel(parent)
	}

	return context.WithDeadline(parent, deadline)
}

func (s *AgentSession) validateRequestContext(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("agent session is nil")
	}
	if ctx == nil {
		return fmt.Errorf("agent request context is nil")
	}
	s.mu.Lock()
	stopping := s.stoppingID != ""
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("agent session is closed")
	}
	if stopping {
		return fmt.Errorf("agent session is stopping")
	}

	return ctx.Err()
}

func remoteError(err error) error {
	if err == nil {
		return nil
	}

	grpcStatus, ok := status.FromError(err)
	if !ok {
		return err
	}
	for _, detail := range grpcStatus.Details() {
		typed, ok := detail.(*agentv1.ErrorDetail)
		if !ok {
			continue
		}
		code, codeErr := protocol.ErrorCodeFromAgent(typed.GetCode())
		if codeErr != nil {
			return err
		}

		return RemoteError{
			CorrelationID: protocol.CorrelationID(
				typed.GetCorrelationId(),
			),
			Code:      code,
			Message:   typed.GetMessage(),
			Retryable: typed.GetRetryable(),
		}
	}

	return err
}

func remoteRequestError(
	err error,
	expected protocol.CorrelationID,
) error {
	remote := remoteError(err)

	var typed RemoteError
	if !errors.As(remote, &typed) {
		return remote
	}
	if typed.CorrelationID != expected {
		return fmt.Errorf(
			"agent error correlation ID %q does not match %q",
			typed.CorrelationID,
			expected,
		)
	}

	return remote
}
