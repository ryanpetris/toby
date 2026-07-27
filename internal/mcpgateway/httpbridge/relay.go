package httpbridge

// Relays protocol messages in both directions after transport setup.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxConcurrentWrites = 64
	writeDrainTimeout   = 250 * time.Millisecond
)

type relayResult struct {
	side string
	err  error
}

func relayDownstream(
	ctx context.Context,
	downstream mcp.Connection,
	upstream mcp.Connection,
	state *sessionState,
	calls *callTracker,
) error {
	relayContext, cancel := context.WithCancelCause(ctx)

	limit := make(chan struct{}, maxConcurrentWrites)
	var writes sync.WaitGroup
	defer func() {
		cancel(nil)
		writes.Wait()
	}()

	for {
		message, err := downstream.Read(relayContext)
		if err != nil {
			if cause := context.Cause(relayContext); cause != nil {
				return cause
			}
			if errors.Is(err, io.EOF) {
				if err := drainRelayWrites(
					relayContext,
					cancel,
					&writes,
					writeDrainTimeout,
				); err != nil {
					return err
				}
			}
			return err
		}

		state.observeDownstream(message)
		if !state.initializationComplete() {
			if err := writeUpstream(
				relayContext,
				upstream,
				state,
				calls,
				message,
			); err != nil {
				return err
			}
			continue
		}

		select {
		case limit <- struct{}{}:
		case <-relayContext.Done():
			return context.Cause(relayContext)
		default:
			return errors.New("MCP HTTP session exceeded the concurrent write limit")
		}

		writes.Add(1)
		receipt := newDispatchReceipt()
		go func() {
			defer writes.Done()
			defer func() {
				<-limit
			}()
			defer receipt.signal()

			writeContext := withDispatchReceipt(relayContext, receipt)
			if err := writeUpstream(
				writeContext,
				upstream,
				state,
				calls,
				message,
			); err != nil {
				cancel(err)
			}
		}()
		// Wait until the HTTP transport reports that the request and its body
		// were written. This preserves source dispatch order while still letting
		// later cancellation messages bypass a long-running response.
		if err := receipt.wait(relayContext); err != nil {
			return err
		}
	}
}

func writeUpstream(
	ctx context.Context,
	upstream mcp.Connection,
	state *sessionState,
	calls *callTracker,
	message jsonrpc.Message,
) error {
	callID, tracked, err := calls.acquire(message)
	if err != nil {
		return err
	}

	if err := upstream.Write(ctx, message); err != nil {
		if tracked {
			calls.release(callID)
		}
		return err
	}

	state.observeSessionID(upstream.SessionID())
	state.observeAcceptedDownstream(message)
	return nil
}

func relayUpstream(
	ctx context.Context,
	upstream mcp.Connection,
	downstream mcp.Connection,
	state *sessionState,
	calls *callTracker,
) error {
	for {
		message, err := upstream.Read(ctx)
		if err != nil {
			if state.hasMessageLimitError() {
				return state.messageLimitError()
			}
			return err
		}

		if err := state.observeUpstream(message); err != nil {
			return fmt.Errorf("inspect MCP initialize response: %w", err)
		}
		calls.complete(message)
		if err := downstream.Write(ctx, message); err != nil {
			return err
		}
	}
}

func drainRelayWrites(
	ctx context.Context,
	cancel context.CancelCauseFunc,
	writes *sync.WaitGroup,
	timeout time.Duration,
) error {
	done := make(chan struct{})
	go func() {
		writes.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return context.Cause(ctx)
	case <-ctx.Done():
		<-done
		return context.Cause(ctx)
	case <-timer.C:
		timeoutErr := fmt.Errorf(
			"drain accepted MCP HTTP writes within %s",
			timeout,
		)
		cancel(timeoutErr)
		<-done
		return timeoutErr
	}
}

func relayError(result relayResult) error {
	if result.err == nil {
		return nil
	}
	if result.side == "downstream" && errors.Is(result.err, io.EOF) {
		return nil
	}

	return fmt.Errorf("%s MCP relay: %w", result.side, result.err)
}
