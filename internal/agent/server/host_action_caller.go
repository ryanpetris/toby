package server

// Multiplexes agent-originated host actions over the client-opened session.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	agentv1 "petris.dev/toby/internal/gen/toby/agent/v1"
)

type hostActionCaller struct {
	session *agentSession
	options Options

	mu      sync.Mutex
	active  bool
	revoked bool
	pending map[protocol.CorrelationID]chan hostActionOutcome
	done    chan struct{}
}

var _ HostActionCaller = (*hostActionCaller)(nil)

type hostActionOutcome struct {
	payload json.RawMessage
	err     error
}

// HostActionError is a bounded launch-side transport failure. Logical action
// errors remain inside the encoded host action response.
type HostActionError struct {
	Code      protocol.ErrorCode
	Message   string
	Retryable bool
}

// Error returns the human-readable failure message.
func (e HostActionError) Error() string {
	return fmt.Sprintf("host action %s: %s", e.Code, e.Message)
}

func newHostActionCaller(
	session *agentSession,
	options Options,
) *hostActionCaller {
	return &hostActionCaller{
		session: session,
		options: options,
		pending: make(
			map[protocol.CorrelationID]chan hostActionOutcome,
		),
		done: make(chan struct{}),
	}
}

func (c *hostActionCaller) activate() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.revoked {
		return io.ErrClosedPipe
	}
	c.active = true

	return nil
}

func (c *hostActionCaller) Call(
	ctx context.Context,
	payload json.RawMessage,
) (json.RawMessage, error) {
	if c == nil || c.session == nil {
		return nil, fmt.Errorf("host action caller is not configured")
	}
	if ctx == nil {
		return nil, fmt.Errorf("host action context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := protocol.ValidateHostActionPayload(payload); err != nil {
		return nil, err
	}

	id, err := protocol.NewCorrelationID()
	if err != nil {
		return nil, err
	}
	outcome := make(chan hostActionOutcome, 1)

	c.mu.Lock()
	if !c.active || c.revoked {
		c.mu.Unlock()
		return nil, io.ErrClosedPipe
	}
	c.pending[id] = outcome
	c.mu.Unlock()

	if err := c.session.send(&agentv1.SessionServerMessage{
		CorrelationId: string(id),
		Value: &agentv1.SessionServerMessage_HostActionRequest{
			HostActionRequest: &agentv1.HostActionRequest{
				Payload: append([]byte(nil), payload...),
			},
		},
	}); err != nil {
		c.remove(id)
		return nil, fmt.Errorf("send host action request: %w", err)
	}

	select {
	case result := <-outcome:
		return append(json.RawMessage(nil), result.payload...), result.err
	case <-ctx.Done():
		if c.remove(id) {
			c.options.Logger.DebugError(
				"cancel expired host action request",
				c.cancel(id),
				"correlation_id", id,
			)
		}
		return nil, ctx.Err()
	case <-c.done:
		c.remove(id)
		return nil, io.ErrClosedPipe
	}
}

func (c *hostActionCaller) deliverResponse(
	id protocol.CorrelationID,
	payload []byte,
) error {
	if err := protocol.ValidateHostActionPayload(payload); err != nil {
		return fmt.Errorf("validate host action response: %w", err)
	}

	return c.deliver(id, hostActionOutcome{
		payload: append(json.RawMessage(nil), payload...),
	})
}

func (c *hostActionCaller) deliverError(
	id protocol.CorrelationID,
	err HostActionError,
) error {
	return c.deliver(id, hostActionOutcome{err: err})
}

func (c *hostActionCaller) deliver(
	id protocol.CorrelationID,
	outcome hostActionOutcome,
) error {
	c.mu.Lock()
	pending := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if pending == nil {
		return nil
	}

	pending <- outcome
	return nil
}

func (c *hostActionCaller) remove(id protocol.CorrelationID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, found := c.pending[id]
	delete(c.pending, id)
	return found
}

func (c *hostActionCaller) cancel(id protocol.CorrelationID) error {
	return c.session.send(&agentv1.SessionServerMessage{
		CorrelationId: string(id),
		Value: &agentv1.SessionServerMessage_HostActionCancel{
			HostActionCancel: &agentv1.HostActionCancel{},
		},
	})
}

func (c *hostActionCaller) revoke() {
	if c == nil {
		return
	}

	c.mu.Lock()
	if c.revoked {
		c.mu.Unlock()
		return
	}
	c.active = false
	c.revoked = true
	clear(c.pending)
	close(c.done)
	c.mu.Unlock()
}

func boundedServerContext(
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
