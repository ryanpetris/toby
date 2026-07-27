package client

// Verifies typed gRPC errors retain request correlation.

import (
	"strings"
	"testing"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRemoteRequestErrorRejectsWrongCorrelation(t *testing.T) {
	base := status.New(codes.Internal, "failed")
	detailed, err := base.WithDetails(&agentv1.ErrorDetail{
		CorrelationId: "other",
		Code:          agentv1.ErrorCode_ERROR_CODE_INTERNAL,
		Message:       "failed",
	})
	if err != nil {
		t.Fatal(err)
	}

	remote := remoteRequestError(
		detailed.Err(),
		protocol.CorrelationID("expected"),
	)
	if !strings.Contains(remote.Error(), "does not match") {
		t.Fatalf("remote error = %v, want correlation mismatch", remote)
	}
}
