//go:build linux

package run

// Declares host-network files required by application sandboxes.

import (
	"fmt"

	"petris.dev/toby/internal/sandbox/bwrap"
	"petris.dev/toby/internal/sandbox/mount"
)

const nativeResolverPath = "/etc/resolv.conf"

func addNativeResolverBind(sandbox *bwrap.ToolSandbox) error {
	if sandbox == nil {
		return fmt.Errorf("configure sandbox DNS resolver: sandbox is nil")
	}
	if err := sandbox.AddBind(mount.Bind{
		HostPath: nativeResolverPath,
		Target:   nativeResolverPath,
		Access:   mount.AccessReadOnly,
	}); err != nil {
		return fmt.Errorf("configure sandbox DNS resolver: %w", err)
	}
	return nil
}
