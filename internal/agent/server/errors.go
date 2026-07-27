package server

// Constructs bounded gRPC errors with typed agent-protocol details.

import (
	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func agentError(
	correlationID string,
	code protocol.ErrorCode,
	message string,
	retryable bool,
) error {
	grpcCode := codes.Internal
	switch code {
	case protocol.ErrorInvalidRequest:
		grpcCode = codes.InvalidArgument
	case protocol.ErrorAcquireFailed:
		grpcCode = codes.Unavailable
	case protocol.ErrorLeaseNotFound:
		grpcCode = codes.NotFound
	case protocol.ErrorUnavailable:
		grpcCode = codes.Unavailable
	case protocol.ErrorInternal:
		grpcCode = codes.Internal
	}

	base := status.New(grpcCode, message)
	detailed, err := base.WithDetails(&agentv1.ErrorDetail{
		CorrelationId: correlationID,
		Code:          protocol.ErrorCodeToAgent(code),
		Message:       message,
		Retryable:     retryable,
	})
	if err != nil {
		return base.Err()
	}

	return detailed.Err()
}

func invalidRequest(correlationID string, message string) error {
	return agentError(
		correlationID,
		protocol.ErrorInvalidRequest,
		message,
		false,
	)
}
