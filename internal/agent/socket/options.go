package socket

// Defines diagnostics used while opening private socket endpoints.

import "petris.dev/toby/internal/diagnostic"

// Options configures best-effort socket diagnostics.
type Options struct {
	Logger *diagnostic.Logger
}
