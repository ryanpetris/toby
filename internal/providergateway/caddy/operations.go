package caddy

// Implements the fixed, bounded Caddy administration request set.

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

const (
	adminOrigin = "http://caddy-admin.invalid"
	configPath  = "/config/admin/config/persist"
	loadPath    = "/load"
	stopPath    = "/stop"
)

// Probe verifies that Caddy's configuration endpoint is ready.
func (c *Client) Probe(ctx context.Context) error {
	body, err := c.perform(
		ctx,
		operationProbe,
		http.MethodGet,
		configPath,
		nil,
		"",
	)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(body), []byte("false")) {
		return ErrProtocol
	}

	return nil
}

// Load atomically submits one complete native JSON configuration.
func (c *Client) Load(ctx context.Context, config []byte) error {
	if ctx == nil {
		return ErrInvalidRequest
	}
	if c == nil || c.maxConfigBytes <= 0 {
		return ErrUnavailable
	}
	if int64(len(config)) > c.maxConfigBytes {
		return ErrConfigTooLarge
	}

	_, err := c.perform(
		ctx,
		operationLoad,
		http.MethodPost,
		loadPath,
		bytes.NewReader(config),
		"application/json",
	)
	return err
}

// Stop asks Caddy to gracefully stop its active configuration and process.
func (c *Client) Stop(ctx context.Context) error {
	_, err := c.perform(
		ctx,
		operationStop,
		http.MethodPost,
		stopPath,
		nil,
		"",
	)
	return err
}

type operation uint8

const (
	operationProbe operation = iota
	operationLoad
	operationStop
)

func (c *Client) perform(
	ctx context.Context,
	operation operation,
	method string,
	path string,
	body io.Reader,
	contentType string,
) ([]byte, error) {
	if ctx == nil {
		return nil, ErrInvalidRequest
	}
	if c == nil ||
		c.httpClient == nil ||
		c.maxResponseBytes <= 0 ||
		c.requestTimeout <= 0 ||
		c.closed.Load() {
		return nil, ErrUnavailable
	}

	requestCtx, cancel := context.WithTimeout(
		ctx,
		c.requestTimeout,
	)
	defer cancel()

	request, err := http.NewRequestWithContext(
		requestCtx,
		method,
		adminOrigin+path,
		body,
	)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		result := classifyOperationError(requestCtx, err)
		if response != nil && response.Body != nil {
			c.logger.DebugError(
				"close failed Caddy administration response",
				response.Body.Close(),
				"operation", operation,
			)
		}
		return nil, result
	}
	if response.StatusCode != http.StatusOK {
		result := operationStatusError(
			operation,
			response.StatusCode,
		)
		c.logger.DebugError(
			"close rejected Caddy administration response",
			response.Body.Close(),
			"operation", operation,
			"http_status", response.StatusCode,
		)
		return nil, result
	}

	var responseBody []byte
	var received int64
	var oversized bool
	var readErr error
	if operation == operationProbe {
		limit := min(c.maxResponseBytes, maxProbeBodyBytes)
		responseBody, oversized, readErr = readBody(
			response.Body,
			limit,
		)
		received = int64(len(responseBody))
	} else {
		received, oversized, readErr = discardBody(
			response.Body,
			c.maxResponseBytes,
		)
	}
	closeErr := response.Body.Close()
	if readErr != nil {
		c.logger.DebugError(
			"close Caddy administration response",
			closeErr,
			"operation", operation,
		)
		return nil, classifyOperationError(requestCtx, readErr)
	}
	c.logger.DebugError(
		"close Caddy administration response",
		closeErr,
		"operation", operation,
	)
	if oversized {
		return nil, ErrResponseTooLarge
	}
	if operation != operationProbe && received != 0 {
		return nil, ErrProtocol
	}

	return responseBody, nil
}

func discardBody(body io.Reader, limit int64) (int64, bool, error) {
	written, err := io.Copy(
		io.Discard,
		io.LimitReader(body, limit+1),
	)
	if err != nil {
		return 0, false, err
	}

	return written, written > limit, nil
}

func readBody(
	body io.Reader,
	limit int64,
) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, false, err
	}

	return data, int64(len(data)) > limit, nil
}

func operationStatusError(operation operation, status int) error {
	if status >= http.StatusInternalServerError {
		return ErrUnavailable
	}
	if operation == operationLoad && status == http.StatusBadRequest {
		return ErrRejected
	}

	return ErrProtocol
}
