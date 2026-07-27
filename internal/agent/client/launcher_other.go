//go:build !linux

package client

// Reports that the agent runtime is Linux-only.

import (
	"fmt"

	"petris.dev/toby/internal/agent/socket"
	"petris.dev/toby/internal/diagnostic"
)

func launchDetached(string, []string, *diagnostic.Logger) error {
	return fmt.Errorf(
		"%w: agent autostart requires Linux",
		socket.ErrUnsupported,
	)
}
