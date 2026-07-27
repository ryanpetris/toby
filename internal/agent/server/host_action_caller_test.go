package server

// Verifies late host-action results do not invalidate a live session.

import (
	"testing"

	"petris.dev/toby/internal/agent/protocol"
)

func TestHostActionCallerIgnoresLateResultAfterCancellation(t *testing.T) {
	caller := &hostActionCaller{
		pending: make(
			map[protocol.CorrelationID]chan hostActionOutcome,
		),
	}

	if err := caller.deliverResponse(
		"completed-after-cancel",
		[]byte(`{"result":"ignored"}`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestHostActionCallerRejectsInvalidResponse(t *testing.T) {
	caller := &hostActionCaller{
		pending: make(
			map[protocol.CorrelationID]chan hostActionOutcome,
		),
	}

	if err := caller.deliverResponse(
		"invalid",
		[]byte(`not-json`),
	); err == nil {
		t.Fatal("invalid host-action response succeeded")
	}
}
