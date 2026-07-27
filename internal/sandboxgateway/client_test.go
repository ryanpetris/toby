package sandboxgateway

// Tests client-side sandbox protocol selection before resource streaming.

import (
	"errors"
	"strings"
	"testing"

	sandboxv1 "petris.dev/toby/internal/gen/toby/sandbox/v1"
	sandboxprotocol "petris.dev/toby/internal/sandboxgateway/protocol"
)

func TestValidateHelloResponse(t *testing.T) {
	const correlationID = "correlation"

	for _, test := range []struct {
		name     string
		response *sandboxv1.HelloResponse
		match    string
		target   error
	}{
		{
			name: "supported",
			response: &sandboxv1.HelloResponse{
				CorrelationId:   correlationID,
				BinaryVersion:   "dev",
				ProtocolVersion: sandboxprotocol.Version,
			},
		},
		{
			name:  "missing response",
			match: "no response",
		},
		{
			name: "changed correlation",
			response: &sandboxv1.HelloResponse{
				CorrelationId:   "different",
				BinaryVersion:   "dev",
				ProtocolVersion: sandboxprotocol.Version,
			},
			match: "does not match",
		},
		{
			name: "missing binary version",
			response: &sandboxv1.HelloResponse{
				CorrelationId:   correlationID,
				ProtocolVersion: sandboxprotocol.Version,
			},
			match: "omitted the binary version",
		},
		{
			name: "unsupported protocol",
			response: &sandboxv1.HelloResponse{
				CorrelationId:   correlationID,
				BinaryVersion:   "dev",
				ProtocolVersion: sandboxprotocol.Version + 1,
			},
			target: sandboxprotocol.ErrVersionMismatch,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateHelloResponse(correlationID, test.response)
			if test.match == "" && test.target == nil {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if test.target != nil && !errors.Is(err, test.target) {
				t.Fatalf("error = %v, want %v", err, test.target)
			}
			if test.match != "" &&
				(err == nil || !strings.Contains(err.Error(), test.match)) {
				t.Fatalf("error = %v, want containing %q", err, test.match)
			}
		})
	}
}
