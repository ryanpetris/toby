package client

// Dispatches reverse host actions received over one agent session.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

func (s *AgentSession) dispatchHostAction(
	message *agentv1.SessionServerMessage,
) {
	id := protocol.CorrelationID(message.GetCorrelationId())
	if err := protocol.ValidateCorrelationID(id); err != nil {
		s.setCloseError(fmt.Errorf(
			"agent sent an invalid host action correlation ID",
		))
		s.abort(nil)
		return
	}
	payload := append(
		json.RawMessage(nil),
		message.GetHostActionRequest().GetPayload()...,
	)
	if err := protocol.ValidateHostActionPayload(payload); err != nil {
		clear(payload)
		s.setCloseError(fmt.Errorf(
			"agent sent an invalid host action payload",
		))
		s.abort(nil)
		return
	}

	s.mu.Lock()
	if _, duplicate := s.active[id]; duplicate {
		s.mu.Unlock()
		s.setCloseError(fmt.Errorf(
			"agent duplicated active host action correlation ID %q",
			id,
		))
		s.abort(nil)
		return
	}
	if s.closed || s.handler == nil {
		s.mu.Unlock()
		clear(payload)
		s.writeHostActionError(
			id,
			protocol.ErrorUnavailable,
			"host action handling is unavailable",
		)
		return
	}

	select {
	case s.hostActionPermits <- struct{}{}:
	default:
		s.mu.Unlock()
		clear(payload)
		s.writeHostActionError(
			id,
			protocol.ErrorUnavailable,
			"host action concurrency limit reached",
		)
		return
	}

	ctx, cancel := context.WithCancel(s.hostActionContext)
	s.active[id] = cancel
	handler := s.handler
	s.hostActionHandlers.Add(1)
	s.mu.Unlock()

	go s.runHostAction(ctx, id, payload, handler)
}

func (s *AgentSession) runHostAction(
	ctx context.Context,
	id protocol.CorrelationID,
	payload json.RawMessage,
	handler HostActionHandler,
) {
	defer s.hostActionHandlers.Done()
	defer clear(payload)
	defer func() {
		<-s.hostActionPermits

		s.mu.Lock()
		delete(s.active, id)
		s.mu.Unlock()
	}()

	response, err := handler.Handle(ctx, payload)
	defer clear(response)
	if ctx.Err() != nil {
		return
	}
	if len(response) == 0 {
		s.writeHostActionError(
			id,
			protocol.ErrorInternal,
			"host action dispatch failed",
		)
		return
	}
	if err := protocol.ValidateHostActionPayload(response); err != nil {
		s.writeHostActionError(
			id,
			protocol.ErrorInternal,
			"host action dispatch returned an invalid response",
		)
		return
	}

	writeErr := s.send(&agentv1.SessionClientMessage{
		CorrelationId: string(id),
		Value: &agentv1.SessionClientMessage_HostActionResponse{
			HostActionResponse: &agentv1.HostActionResponse{
				Payload: append([]byte(nil), response...),
			},
		},
	})
	if writeErr != nil {
		s.setCloseError(errors.Join(err, writeErr))
		s.abort(nil)
	}
}

func (s *AgentSession) writeHostActionError(
	id protocol.CorrelationID,
	code protocol.ErrorCode,
	message string,
) {
	err := s.send(&agentv1.SessionClientMessage{
		CorrelationId: string(id),
		Value: &agentv1.SessionClientMessage_HostActionError{
			HostActionError: &agentv1.HostActionError{
				Code:      protocol.ErrorCodeToAgent(code),
				Message:   message,
				Retryable: false,
			},
		},
	})
	if err != nil {
		s.setCloseError(err)
		s.abort(nil)
	}
}

func (s *AgentSession) cancelHostAction(id protocol.CorrelationID) {
	s.mu.Lock()
	cancel := s.active[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
