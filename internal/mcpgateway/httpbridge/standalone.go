package httpbridge

// Maintains and resumes the standalone SSE stream for one HTTP session.

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"petris.dev/toby/internal/diagnostic"
)

const (
	defaultEventRetry     = time.Second
	maximumConnectRetries = 5
)

func receiveStandalone(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	state *sessionState,
	downstream mcp.Connection,
	calls *callTracker,
	maxEventSize int,
	logger *diagnostic.Logger,
) error {
	select {
	case <-ctx.Done():
		return nil
	case <-state.waitReady():
	}

	identity := state.identity()
	cursor := eventCursor{retry: defaultEventRetry}
	opened := false
	failures := 0

	for {
		response, err := openStandalone(
			ctx,
			client,
			endpoint,
			identity,
			cursor.lastEventID,
		)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			failures++
			if failures > maximumConnectRetries {
				return fmt.Errorf(
					"open standalone MCP event stream after %d retries: %w",
					maximumConnectRetries,
					err,
				)
			}
			if !waitForEventRetry(ctx, reconnectDelay(cursor.retry, failures)) {
				return nil
			}
			continue
		}

		if response.StatusCode == http.StatusMethodNotAllowed ||
			(!opened &&
				response.StatusCode >= http.StatusBadRequest &&
				response.StatusCode < http.StatusInternalServerError) {
			logger.DebugError(
				"close unsupported standalone MCP event response",
				response.Body.Close(),
			)
			return nil
		}
		if response.StatusCode < http.StatusOK ||
			response.StatusCode >= http.StatusMultipleChoices {
			status := response.StatusCode
			logger.DebugError(
				"close failed standalone MCP event response",
				response.Body.Close(),
				"http_status", status,
			)

			failures++
			if status >= http.StatusInternalServerError &&
				failures <= maximumConnectRetries {
				if !waitForEventRetry(
					ctx,
					reconnectDelay(cursor.retry, failures),
				) {
					return nil
				}
				continue
			}

			return fmt.Errorf(
				"open standalone MCP event stream: unexpected HTTP status %d",
				status,
			)
		}

		mediaType, _, err := mime.ParseMediaType(
			response.Header.Get("Content-Type"),
		)
		if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
			logger.DebugError(
				"close invalid standalone MCP event response",
				response.Body.Close(),
			)
			return errors.New(
				"open standalone MCP event stream: response is not text/event-stream",
			)
		}

		opened = true
		failures = 0
		readErr := scanEventMessages(
			response.Body,
			maxEventSize,
			&cursor,
			func(message jsonrpc.Message) error {
				calls.complete(message)
				return downstream.Write(ctx, message)
			},
		)
		logger.DebugError(
			"close standalone MCP event response",
			response.Body.Close(),
		)

		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(readErr, ErrMessageTooLarge) ||
			errors.Is(readErr, errEventProtocol) ||
			errors.Is(readErr, errEventConsumer) {
			return fmt.Errorf(
				"read standalone MCP event stream: %w",
				readErr,
			)
		}
		if !waitForEventRetry(ctx, cursor.retry) {
			return nil
		}
	}
}

func openStandalone(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	identity sessionIdentity,
	lastEventID string,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		endpoint,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create standalone MCP event request: %w", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	if identity.sessionID != "" {
		request.Header.Set(sessionIDHeader, identity.sessionID)
	}
	request.Header.Set(protocolVersionHeader, identity.protocolVersion)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func reconnectDelay(base time.Duration, failures int) time.Duration {
	delay := base
	for range max(failures-1, 0) {
		if delay >= maximumEventRetry/2 {
			return maximumEventRetry
		}
		delay *= 2
	}
	return min(delay, maximumEventRetry)
}

func waitForEventRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
