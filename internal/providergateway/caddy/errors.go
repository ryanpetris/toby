package caddy

// Defines stable administration errors without retaining transport or Caddy
// response details.

import (
	"context"
	"errors"
	"net"
)

var (
	// ErrInvalidOptions indicates that the client was constructed with an
	// invalid connector or limit.
	ErrInvalidOptions = errors.New(
		"caddy admin client configuration is invalid",
	)

	// ErrInvalidRequest indicates that an operation received invalid caller
	// input.
	ErrInvalidRequest = errors.New("caddy admin request is invalid")

	// ErrConfigTooLarge indicates that a native configuration exceeds the
	// configured request-body limit.
	ErrConfigTooLarge = errors.New(
		"caddy admin configuration exceeds the byte limit",
	)

	// ErrUnavailable indicates that the protected administration endpoint
	// could not complete a request.
	ErrUnavailable = errors.New("caddy admin endpoint is unavailable")

	// ErrRequestCanceled indicates that the caller canceled an administration
	// request.
	ErrRequestCanceled = errors.New("caddy admin request was canceled")

	// ErrRequestTimeout indicates that an administration request exceeded its
	// total deadline.
	ErrRequestTimeout = errors.New("caddy admin request timed out")

	// ErrResponseTooLarge indicates that Caddy's response exceeded the bounded
	// amount the client will discard.
	ErrResponseTooLarge = errors.New(
		"caddy admin response exceeds the byte limit",
	)

	// ErrRejected indicates that Caddy rejected a submitted native
	// configuration.
	ErrRejected = errors.New("caddy admin request was rejected")

	// ErrProtocol indicates that Caddy returned an undocumented status or
	// response shape.
	ErrProtocol = errors.New("caddy admin protocol response is invalid")
)

var errNonUnixConnection = errors.New(
	"caddy admin connector returned a non-Unix connection",
)

func classifyOperationError(ctx context.Context, err error) error {
	switch {
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled):
		return errors.Join(ErrRequestCanceled, context.Canceled)
	case ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		return errors.Join(ErrRequestTimeout, context.DeadlineExceeded)
	case errors.Is(err, context.Canceled):
		return errors.Join(ErrRequestCanceled, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return errors.Join(ErrRequestTimeout, context.DeadlineExceeded)
	}

	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return errors.Join(ErrRequestTimeout, context.DeadlineExceeded)
	}

	return ErrUnavailable
}
