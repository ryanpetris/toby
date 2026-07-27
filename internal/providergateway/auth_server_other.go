//go:build !linux

package providergateway

// Rejects provider authorization service construction on unsupported hosts.

import (
	"context"
	"fmt"

	"petris.dev/toby/internal/diagnostic"
)

type authServerOptions struct {
	Logger *diagnostic.Logger
}

type authServer struct{}

func newAuthServer(
	context.Context,
	string,
	*routeStore,
	authServerOptions,
) (*authServer, error) {
	return nil, fmt.Errorf(
		"provider authorization service requires Linux",
	)
}
